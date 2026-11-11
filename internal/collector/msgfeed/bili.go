package msgfeed

// 本文件把 bili 客户端适配成 Fetcher（真实账号消息拉取）。
//
// 覆盖承诺：
//   - 每轮同时拉「回复我的」和「@我的」，各自向旧翻页到水位为止（单轮最多 MaxPages 页）；
//   - 增量按「已处理水位」拉取：游标之后的消息全部产出（含平台已读），不依赖未读状态；
//   - 首次接入没有水位时：平台有 last_view_at 就按类别各自划基线；
//     否则以该类别当前最新一条为基线，不补答历史；
//   - 平台 last_view_at 只用于基线与顶号观测，绝不拿来跳过消息；
//   - 水位按账号隔离；整轮用绑定的账号快照发请求，不会中途切号；
//   - 一段拉取失败就整轮不产出，下一轮重试；不提交半截水位。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"RaiseMyFish/internal/bili"
	"go.uber.org/zap"
)

const (
	defaultMaxPages = 5
	maxPagesUpper   = 20
)

// ActorSource 提供当前账号快照（account_id + self_id + 凭据）。
type ActorSource interface {
	CurrentActor(ctx context.Context) (bili.Actor, error)
}

// Admission 是账号切换准入；整轮采集持有，Batch.Close 释放。
type Admission interface {
	BeginOperation(ctx context.Context) (func(), error)
}

// BiliSource 通过 bili 客户端拉 msgfeed。
type BiliSource struct {
	Client *bili.Client
	// Actors 非空时每轮取一次账号快照；为空时用客户端当前凭据自行快照。
	Actors ActorSource
	// Admission 非空时整轮持有账号准入，避免拉取中途切号。
	Admission Admission
	// SelfID 是快照缺 MID 时的兜底。
	SelfID string
	// Cursors 为空时只保留进程内水位。
	Cursors CursorStore
	// MaxPages 是单类消息每轮最多翻页数（<=0 用 5）。
	MaxPages int
	Now      func() time.Time
	Log      *zap.Logger
}

type fetchedFeed struct {
	items   []bili.MsgfeedItem
	newest  bili.MsgfeedItem
	hasTime bool
}

// Fetch 实现 Fetcher；返回的 Batch.Commit 在整批事件落库确认后调用。
func (s *BiliSource) Fetch(ctx context.Context) (Batch, error) {
	if s == nil || s.Client == nil {
		return Batch{}, &Error{Kind: KindAuth, Cause: bili.ErrNoCredential}
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	release, err := s.begin(ctx)
	if err != nil {
		return Batch{}, err
	}
	// 未走到成功返回（任一拉取失败）都要立即归还准入。
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	actor, err := s.snapshot(ctx)
	if err != nil {
		return Batch{}, err
	}
	selfID := strings.TrimSpace(actor.MID)
	if !validMID(selfID) {
		selfID = strings.TrimSpace(s.SelfID)
	}
	if !validMID(selfID) {
		return Batch{}, &Error{Kind: KindAuth, Cause: bili.ErrNoCredential}
	}
	finish := func(messages []Message, cursor Cursor) Batch {
		b := s.batch(selfID, messages, cursor)
		b.Close = release
		handedOff = true
		return b
	}
	client, err := s.Client.Bind(actor)
	if err != nil {
		return Batch{}, &Error{Kind: KindAuth, Cause: bili.ErrNoCredential}
	}
	cursor := Cursor{}
	if s.Cursors != nil {
		cursor, err = s.Cursors.Load(selfID)
		if err != nil {
			return Batch{}, &Error{Kind: KindInput, Cause: err}
		}
	}
	if !validCursor(cursor) {
		cursor = Cursor{}
	}

	// 先各拉第一页：首次接入要用 last_view_at / 最新一条建立基线。
	replyPage, err := client.MsgfeedReplies(ctx, "", 0)
	if err != nil {
		return Batch{}, classify("拉回复消息", err)
	}
	atPage, err := client.MsgfeedAts(ctx, "", 0)
	if err != nil {
		return Batch{}, classify("拉@消息", err)
	}

	next := cursor
	viewAt := max(replyPage.LastViewAt, atPage.LastViewAt)
	if !cursor.Initialized {
		// 首次接入：两类消息各用自己的 last_view_at 建基线，互不连坐；
		// 没有查看水位的类别退回自身最新一条（或当前时刻），历史不补答。
		replyBase, replyID := baselineFor(replyPage, now())
		atBase, atID := baselineFor(atPage, now())
		next = Cursor{
			Initialized: true,
			ReplyTime:   replyBase,
			ReplyID:     replyID,
			AtTime:      atBase,
			AtID:        atID,
			LastViewAt:  viewAt,
		}
	} else {
		// 查看水位只观测、不推进：主人顶号把消息标已读会推高 last_view_at，
		// 增量仍按已处理游标拉，已读消息照常采集。
		if processed := max(cursor.ReplyTime, cursor.AtTime); viewAt > cursor.LastViewAt && viewAt > processed && s.Log != nil {
			s.Log.Warn("消息查看水位超前，仍按已处理游标拉取",
				zap.String("action", "msgfeed_watermark_ahead"),
				zap.Int64("delta_seconds", viewAt-processed),
			)
		}
		next.LastViewAt = max(cursor.LastViewAt, viewAt)
	}
	// 坏水位/旧版本水位可能 initialized 但时间为 0：按首次接入重建基线，避免重扫历史。
	if next.ReplyTime <= 0 {
		next.ReplyTime, next.ReplyID = baselineFor(replyPage, now())
	}
	if next.AtTime <= 0 {
		next.AtTime, next.AtID = baselineFor(atPage, now())
	}

	replyFeed, err := s.collectFeed(ctx, client, "reply", replyPage, next.ReplyTime, next.ReplyID)
	if err != nil {
		return Batch{}, err
	}
	atFeed, err := s.collectFeed(ctx, client, "at", atPage, next.AtTime, next.AtID)
	if err != nil {
		return Batch{}, err
	}
	if replyFeed.hasTime {
		next.ReplyTime, next.ReplyID = advance(next.ReplyTime, next.ReplyID, replyFeed.newest.Time, replyFeed.newest.ID)
	}
	if atFeed.hasTime {
		next.AtTime, next.AtID = advance(next.AtTime, next.AtID, atFeed.newest.Time, atFeed.newest.ID)
	}

	messages := make([]Message, 0, len(replyFeed.items)+len(atFeed.items))
	for _, item := range replyFeed.items {
		if msg, ok := toMessage("reply", item); ok {
			messages = append(messages, msg)
		}
	}
	for _, item := range atFeed.items {
		if msg, ok := toMessage("at", item); ok {
			messages = append(messages, msg)
		}
	}
	return finish(messages, next), nil
}

// begin 取得本轮采集的账号切换准入；未注入时为空操作。
func (s *BiliSource) begin(ctx context.Context) (func(), error) {
	if s.Admission == nil {
		return func() {}, nil
	}
	release, err := s.Admission.BeginOperation(ctx)
	if err != nil {
		return nil, &Error{Kind: KindNetwork, Cause: err}
	}
	if release == nil {
		release = func() {}
	}
	return release, nil
}

func (s *BiliSource) batch(selfID string, messages []Message, cursor Cursor) Batch {
	return Batch{
		Messages: messages,
		SelfID:   selfID,
		Commit: func() error {
			if s.Cursors == nil {
				return nil
			}
			// 没有新消息也要落 initialized：下次启动沿用基线，不重扫历史。
			return s.Cursors.Save(selfID, cursor)
		},
	}
}

// snapshot 取本轮账号快照；优先账号服务，其次客户端当前凭据。
func (s *BiliSource) snapshot(ctx context.Context) (bili.Actor, error) {
	var (
		actor bili.Actor
		err   error
	)
	if s.Actors != nil {
		actor, err = s.Actors.CurrentActor(ctx)
	} else {
		actor, err = s.Client.Snapshot(ctx)
	}
	if err != nil {
		if errors.Is(err, bili.ErrNoCredential) {
			return bili.Actor{}, &Error{Kind: KindAuth, Cause: err}
		}
		return bili.Actor{}, &Error{Kind: KindNetwork, Cause: err}
	}
	if !actor.Valid() {
		return bili.Actor{}, &Error{Kind: KindAuth, Cause: bili.ErrNoCredential}
	}
	return actor, nil
}

// collectFeed 从第一页开始向旧翻页，收集水位之后的消息（最多 MaxPages 页）。
func (s *BiliSource) collectFeed(ctx context.Context, client *bili.Client, feed string, first bili.MsgfeedPage, baseTime int64, baseID string) (fetchedFeed, error) {
	maxPages := s.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	if maxPages > maxPagesUpper {
		maxPages = maxPagesUpper
	}
	out := fetchedFeed{items: make([]bili.MsgfeedItem, 0, len(first.Items))}
	page := first
	lastCursorID, lastCursorTime := "", int64(0)
	for pages := 1; ; pages++ {
		hasOld := false
		for _, item := range page.Items {
			if item.Time <= 0 || !validMID(item.ID) {
				continue
			}
			if newerThan(item.Time, item.ID, out.newest.Time, out.newest.ID) || !out.hasTime {
				out.newest, out.hasTime = item, true
			}
			if !after(item.Time, item.ID, baseTime, baseID) {
				hasOld = true
				continue
			}
			out.items = append(out.items, item)
		}
		if page.IsEnd || len(page.Items) == 0 || hasOld {
			break
		}
		if page.CursorID == "" || page.CursorTime <= 0 {
			break
		}
		if page.CursorID == lastCursorID && page.CursorTime == lastCursorTime {
			break
		}
		if pages >= maxPages {
			if s.Log != nil {
				s.Log.Warn("消息监听翻页达到上限，可能有更旧的新消息留待下一轮",
					zap.String("action", "msgfeed_page_limit"),
					zap.String("feed", feed),
					zap.Int("pages", pages),
				)
			}
			break
		}
		lastCursorID, lastCursorTime = page.CursorID, page.CursorTime
		var err error
		if feed == "at" {
			page, err = client.MsgfeedAts(ctx, page.CursorID, page.CursorTime)
		} else {
			page, err = client.MsgfeedReplies(ctx, page.CursorID, page.CursorTime)
		}
		if err != nil {
			return fetchedFeed{}, classify("翻页消息", err)
		}
	}
	return out, nil
}

// toMessage 把消息映射成评论回复目标；at 只接评论里的 @（item.type=reply）。
func toMessage(feed string, item bili.MsgfeedItem) (Message, bool) {
	if feed == "at" && item.ItemType != "reply" {
		return Message{}, false
	}
	if !validMID(item.SubjectID) || !validMID(item.SourceID) || !validMID(item.UserMID) || item.Time <= 0 ||
		strings.Trim(item.SubjectID, "0") == "" || strings.Trim(item.SourceID, "0") == "" {
		return Message{}, false
	}
	return Message{
		Feed:       feed,
		BusinessID: item.BusinessID,
		OID:        item.SubjectID,
		RPID:       item.SourceID,
		Root:       item.RootID,
		FromID:     item.UserMID,
		FromName:   item.UserName,
		Text:       item.Text,
		At:         time.Unix(item.Time, 0),
	}, true
}

// baselineFor 给单类消息选基线：优先平台上次查看时间，其次当前最新一条，最后当前时刻。
func baselineFor(page bili.MsgfeedPage, now time.Time) (int64, string) {
	if page.LastViewAt > 0 {
		return page.LastViewAt, ""
	}
	if newest, ok := newestItem(page.Items); ok {
		return newest.Time, newest.ID
	}
	return now.Unix(), ""
}

func newestItem(pages ...[]bili.MsgfeedItem) (bili.MsgfeedItem, bool) {
	var newest bili.MsgfeedItem
	found := false
	for _, items := range pages {
		for _, item := range items {
			if item.Time <= 0 || !validMID(item.ID) {
				continue
			}
			if !found || newerThan(item.Time, item.ID, newest.Time, newest.ID) {
				newest, found = item, true
			}
		}
	}
	return newest, found
}

// after 判断消息是否严格晚于水位（同秒用通知 ID 比大小）。
func after(t int64, id string, baseTime int64, baseID string) bool {
	if baseTime <= 0 {
		return true
	}
	if t != baseTime {
		return t > baseTime
	}
	return compareIDs(id, baseID) > 0
}

func newerThan(t int64, id string, baseTime int64, baseID string) bool {
	if baseTime <= 0 {
		return t > 0
	}
	if t != baseTime {
		return t > baseTime
	}
	return compareIDs(id, baseID) > 0
}

func advance(baseTime int64, baseID string, t int64, id string) (int64, string) {
	if newerThan(t, id, baseTime, baseID) {
		return t, id
	}
	return baseTime, baseID
}

// compareIDs 比较两个数字 ID：先去掉前导零，再按长度与字典序比较。
func compareIDs(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) > len(b) {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

// classify 只保留受控错误类别，不把响应正文带进错误链。
func classify(op string, err error) error {
	if err == nil {
		return nil
	}
	if bili.IsAuthError(err) {
		return &Error{Kind: KindAuth, Cause: err}
	}
	var typed *bili.Error
	if errors.As(err, &typed) {
		switch typed.Status {
		case 412, 429:
			return &Error{Kind: KindRate, Cause: err}
		}
		switch typed.Code {
		case -352, -412, -509:
			return &Error{Kind: KindRate, Cause: err}
		}
	}
	return &Error{Kind: KindNetwork, Cause: fmt.Errorf("%s失败", op)}
}
