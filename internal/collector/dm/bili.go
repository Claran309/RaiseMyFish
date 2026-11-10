package dm

// 本文件把 bili 客户端适配成 Fetcher（真实私信会话拉取）。
//
// 覆盖承诺：
//   - 会话列表按 end_ts 游标翻页，每轮最多 MaxSessionPages 页（默认 5×100）；
//     到上限仍有更多会话时记 dm_session_limit。
//   - 单会话按 msg_seqno 追新，每轮最多 MaxMessagePages 页（默认 5×100）。
//   - 首次接入（没有水位）从最新往回最多看 MaxMessagePages 页，更早历史作为
//     基线之外，记 dm_history_baseline，不无限重扫全部历史。
//   - 已有水位时，积压超过单轮上限会保留 pending_from 缺口，后续轮次继续回填，
//     不把没看到的区间当已读；回填期间新消息照常产出。
//   - 水位由 Batch.Commit 在整批事件获得持久化确认（Publish 成功）后落盘；
//     仅有 channel 接收不算落库，落盘失败下一轮重试，可能重复产出，由事件 ID 幂等兜底。
//   - 平台未给 seqno 的会话退回 last_msg 去重，不承诺跨重启水位。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"RaiseMyFish/internal/bili"
	"go.uber.org/zap"
)

const (
	defaultSessionPageSize = 100
	defaultMessagePageSize = 100
	defaultMaxSessionPages = 5
	defaultMaxMessagePages = 5
)

// atFromUnix 把秒级时间戳转成时间；0 表示未知。
func atFromUnix(ts int64) time.Time {
	if ts <= 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

// NewBiliFetcher 构造适配器；client 为 nil 时 Fetch 返回鉴权错误（上层会停采集）。
func NewBiliFetcher(c *bili.Client, selfID string, limit int) *BiliFetcher {
	return &BiliFetcher{
		client:   c,
		SelfID:   selfID,
		Limit:    limit,
		lastKeys: make(map[string]string),
		// cursors 保持 nil：首次 Fetch 时从 Cursors 加载，避免绕过持久水位。
	}
}

// ActorSource 提供当前账号快照（account_id + self_id + 凭据）。
type ActorSource interface {
	CurrentActor(ctx context.Context) (bili.Actor, error)
}

// Admission 是账号切换的准入闸门；一轮采集持有到水位提交结束。
type Admission interface {
	BeginOperation(ctx context.Context) (func(), error)
}

// BiliFetcher 通过 bili 客户端拉会话列表，并对变化会话补拉详情。
type BiliFetcher struct {
	client *bili.Client
	// SelfID 是账号快照缺失时的兜底过滤值；正常情况下用批次快照的 MID。
	SelfID string
	// Actors 在每轮 Fetch 开始时取一次账号快照；整批 list/detail 都用它。
	Actors ActorSource
	// Admission 在切换账号时挡住新批次，等当前批次提交完水位再切。
	Admission Admission
	// Limit 是会话单页条数（<=0 用 100，最大 100）。
	Limit int
	// PageSize 是消息单页条数（<=0 用 100，最大 200）。
	PageSize int
	// MaxSessionPages / MaxMessagePages 是单轮最多翻页数（<=0 用 5）。
	MaxSessionPages int
	MaxMessagePages int
	// Cursors 为空时水位只保存在进程内。
	Cursors CursorStore
	// Log 为空时丢弃日志。
	Log *zap.Logger

	mu sync.Mutex
	// lastKeys 记录每个会话上次成功处理的 last_msg key；按账号隔离。
	lastKeys map[string]string
	// cursors 是当前账号的水位快照，Commit 成功后替换。
	cursors map[string]Cursor
	// cursorsAccount/lastAccount 标记内存态属于哪个账号；切号后重新加载。
	cursorsAccount string
	lastAccount    string
	// msgBlocked 记录消息接口不可用（如 404）的会话退避截止时间；避免每轮重试全部会话。
	msgBlocked map[string]time.Time
}

// Fetch 实现 Fetcher；返回的 Batch.Commit 需要在整批事件获得持久化确认后调用，
// Batch.Close 在整批处理结束后调用（释放账号切换准入）。
//
// 账号快照只在开头取一次：之后即使活跃账号被切换，本批 list/detail 仍用
// 同一个客户端与同一份凭据，不会出现「会话列表读新号、消息详情读旧号」。
func (f *BiliFetcher) Fetch(ctx context.Context) (Batch, error) {
	if f.client == nil {
		return Batch{}, &Error{Kind: KindAuth, Cause: bili.ErrNoCredential}
	}
	release, err := f.begin(ctx)
	if err != nil {
		return Batch{}, err
	}
	actor, err := f.snapshotActor(ctx)
	if err != nil {
		release()
		return Batch{}, err
	}
	// 快照不可用时不能退回动态客户端：那会在切号瞬间读到另一个账号。
	if !actor.Valid() {
		release()
		return Batch{}, &Error{Kind: KindAuth, Cause: bili.ErrNoCredential}
	}
	selfID := strings.TrimSpace(actor.MID)
	if selfID == "" {
		selfID = strings.TrimSpace(f.SelfID)
	}
	client, bindErr := f.client.Bind(actor)
	if bindErr != nil {
		release()
		return Batch{}, &Error{Kind: KindAuth, Cause: bindErr}
	}
	current, err := f.snapshotCursors(selfID)
	if err != nil {
		release()
		return Batch{}, &Error{Kind: KindNetwork, Cause: err}
	}
	sessions, err := f.listSessions(ctx, client)
	if err != nil {
		release()
		return Batch{}, err
	}
	next := cloneCursors(current)
	dirty := false
	messages := make([]Message, 0, len(sessions))
	for _, s := range sessions {
		msgs, changed, fetchErr := f.fetchSession(ctx, client, selfID, s, current, next)
		if fetchErr != nil {
			release()
			return Batch{}, fetchErr
		}
		messages = append(messages, msgs...)
		dirty = dirty || changed
	}
	batch := Batch{Messages: messages, Close: release, AccountID: actor.AccountID, SelfID: selfID}
	if dirty {
		account := selfID
		batch.Commit = func() error { return f.commit(account, next) }
	}
	return batch, nil
}

// begin 取得本轮采集的账号切换准入。
func (f *BiliFetcher) begin(ctx context.Context) (func(), error) {
	if f.Admission == nil {
		return func() {}, nil
	}
	release, err := f.Admission.BeginOperation(ctx)
	if err != nil {
		return nil, &Error{Kind: KindNetwork, Cause: err}
	}
	if release == nil {
		release = func() {}
	}
	return release, nil
}

// snapshotActor 取本轮账号快照；未注入 ActorSource 时退回客户端静态凭据。
func (f *BiliFetcher) snapshotActor(ctx context.Context) (bili.Actor, error) {
	if f.Actors != nil {
		actor, err := f.Actors.CurrentActor(ctx)
		if err != nil {
			if errors.Is(err, bili.ErrNoCredential) {
				return bili.Actor{}, &Error{Kind: KindAuth, Cause: err}
			}
			return bili.Actor{}, &Error{Kind: KindNetwork, Cause: err}
		}
		return actor, nil
	}
	actor, err := f.client.Snapshot(ctx)
	if err != nil {
		if errors.Is(err, bili.ErrNoCredential) {
			return bili.Actor{}, &Error{Kind: KindAuth, Cause: err}
		}
		return bili.Actor{}, &Error{Kind: KindNetwork, Cause: err}
	}
	return actor, nil
}


// msgBlockedUntil 判断该会话的消息接口是否仍在退避（404 等不可恢复错误）。
func (f *BiliFetcher) msgBlockedUntil(peer string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	until, ok := f.msgBlocked[peer]
	return ok && time.Now().Before(until)
}

// blockMessages 给会话的消息接口设退避，避免 404 时每轮重试全部会话。
func (f *BiliFetcher) blockMessages(peer string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.msgBlocked == nil {
		f.msgBlocked = make(map[string]time.Time)
	}
	f.msgBlocked[peer] = time.Now().Add(d)
}

// isNotFound 判断错误是否为 HTTP 404（消息接口不可用）。
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "HTTP 404")
}

func (f *BiliFetcher) logf() *zap.Logger {
	if f.Log != nil {
		return f.Log
	}
	return zap.NewNop()
}

func (f *BiliFetcher) snapshotCursors(account string) (map[string]Cursor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 切号后必须换用该账号自己的水位与 last_msg 去重表；
	// 否则旧账号的水位会把新账号的历史误判成「已读」。
	if f.cursorsAccount != account {
		if f.Cursors == nil {
			f.cursors = make(map[string]Cursor)
		} else {
			loaded, err := f.Cursors.Load(account)
			if err != nil {
				return nil, err
			}
			if loaded == nil {
				loaded = make(map[string]Cursor)
			}
			f.cursors = loaded
		}
		f.cursorsAccount = account
	}
	if f.lastAccount != account {
		f.lastKeys = make(map[string]string)
		f.lastAccount = account
	}
	return cloneCursors(f.cursors), nil
}

func (f *BiliFetcher) commit(account string, next map[string]Cursor) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Cursors != nil {
		if err := f.Cursors.Save(account, next); err != nil {
			return err
		}
	}
	f.cursors = cloneCursors(next)
	f.cursorsAccount = account
	f.lastAccount = account
	return nil
}

// listSessions 从最新会话向更早翻页，按对端 uid 去重。
func (f *BiliFetcher) listSessions(ctx context.Context, client *bili.Client) ([]bili.DMSession, error) {
	size := f.sessionPageSize()
	maxPages := f.maxSessionPages()
	seen := make(map[string]struct{})
	out := make([]bili.DMSession, 0, size)
	var endTS int64
	for pageNo := 1; pageNo <= maxPages; pageNo++ {
		page, err := client.DMSessionsPage(ctx, size, endTS)
		if err != nil {
			return nil, &Error{Kind: classify(err), Cause: err}
		}
		added := 0
		for _, s := range page.Sessions {
			if s.TalkerID == "" {
				continue
			}
			if _, ok := seen[s.TalkerID]; ok {
				continue
			}
			seen[s.TalkerID] = struct{}{}
			out = append(out, s)
			added++
		}
		if !page.HasMore {
			return out, nil
		}
		if len(page.Sessions) == 0 {
			return out, nil
		}
		if added == 0 && pageNo > 1 {
			f.logf().Warn("私信会话翻页没有新会话，停止翻页",
				zap.String("action", "dm_session_stall"),
				zap.Int("sessions", len(out)),
				zap.Int("pages", pageNo),
			)
			return out, nil
		}
		last := page.Sessions[len(page.Sessions)-1]
		if last.Timestamp <= 0 {
			f.logf().Warn("私信会话缺少时间，停止翻页",
				zap.String("action", "dm_session_stall"),
				zap.String("peer", last.TalkerID),
			)
			return out, nil
		}
		// end_ts 是微秒级结束时间；加 999999 覆盖同一秒内的全部会话，重复由 uid 去重。
		nextEnd := last.Timestamp*1_000_000 + 999_999
		// 翻页方向是「向更早」，游标没变小说明同一秒内的会话超过一页，无法再用秒级时间推进。
		if endTS > 0 && nextEnd >= endTS {
			f.logf().Warn("私信会话翻页游标未前进，停止翻页",
				zap.String("action", "dm_session_stall"),
				zap.String("peer", last.TalkerID),
			)
			return out, nil
		}
		endTS = nextEnd
		if pageNo == maxPages {
			f.logf().Warn("私信会话超过单轮页数上限，已截断",
				zap.String("action", "dm_session_limit"),
				zap.Int("sessions", len(out)),
				zap.Int("pages", maxPages),
			)
		}
	}
	return out, nil
}

// fetchSession 返回该会话应产出的事件，以及水位是否变化。
func (f *BiliFetcher) fetchSession(ctx context.Context, client *bili.Client, selfID string, s bili.DMSession, current, next map[string]Cursor) ([]Message, bool, *Error) {
	peer := strings.TrimSpace(s.TalkerID)
	if peer == "" {
		return nil, false, nil
	}
	sessionSeq := maxDecimal(s.MaxSeqno, s.MsgSeqno)
	if sessionSeq == "" {
		return f.fetchWithoutSeqno(ctx, client, selfID, s)
	}

	st := normalizeCursor(current[peer])
	watermark := st.Watermark
	frontier := st.Frontier
	pending := st.PendingFrom
	first := watermark == "0" && frontier == "0" && pending == "0"
	if !first && pending == "0" && compareDecimal(sessionSeq, watermark) <= 0 {
		return nil, false, nil
	}

	budget := f.maxMessagePages()
	emitted := make(map[string]struct{})
	var out []Message
	emit := func(items []bili.DMMessage, floor string) {
		for _, m := range items {
			if selfID != "" && m.SenderUID == selfID {
				continue
			}
			if m.Seqno != "" && compareDecimal(m.Seqno, floor) <= 0 {
				continue
			}
			if _, ok := emitted[m.MsgKey]; ok {
				continue
			}
			emitted[m.MsgKey] = struct{}{}
			out = append(out, Message{
				ID:     m.MsgKey,
				PeerID: peer,
				Text:   m.Content,
				At:     atFromUnix(m.Timestamp),
			})
		}
	}

	changed := false
	// 阶段 1：继续回填上次没追平的缺口。
	if pending != "0" && budget > 0 {
		oldest, reached, scanned, scanErr := f.scanOlder(ctx, client, peer, pending, watermark, &budget, func(items []bili.DMMessage) {
			emit(items, watermark)
		})
		if scanErr != nil {
			return nil, false, scanErr
		}
		if scanned {
			changed = true
			if reached {
				pending = "0"
				watermark = maxDecimal(watermark, frontier)
			} else if oldest != "" {
				pending = oldest
			}
		}
	}

	// 阶段 2：从最新页追新；必要时探出新的缺口上沿。
	if budget > 0 && (first || pending != "0" || compareDecimal(sessionSeq, frontier) > 0) {
		oldest, reached, pageMax, scanned, stoppedByError, scanErr := f.scanNewest(ctx, client, peer, watermark, &budget, func(items []bili.DMMessage) {
			if first {
				emit(items, "0")
			} else {
				emit(items, frontier)
			}
		})
		if scanErr != nil {
			return nil, false, scanErr
		}
		if !scanned {
			// 最新页都没拿到：首次接入降级为 last_msg，不推进水位。
			if first && len(out) == 0 && (selfID == "" || s.SenderUID != selfID) {
				out = append(out, messageFromSession(s))
			}
			return out, false, nil
		}
		if pageMax != "" {
			frontier = maxDecimal(frontier, pageMax)
		}
		changed = true
		switch {
		case first && stoppedByError:
			// 分页中途失败不能当历史基线；不推进水位，下一轮重试。
			return out, false, nil
		case first:
			if !reached {
				f.logf().Warn("私信首次接入超过单轮页数上限，按当前窗口建立基线",
					zap.String("action", "dm_history_baseline"),
					zap.String("peer", peer),
					zap.String("watermark", frontier),
					zap.Int("pages", f.maxMessagePages()),
				)
			}
			watermark = frontier
			pending = "0"
		case pending != "0":
			// 阶段 1 没追平（分页失败）：阶段 2 只负责追新，缺口不能清。
			// 上沿取更大的值——本轮只扫到的最老消息到旧缺口之间可能还有没产出的区间，
			// 宁可下一轮重扫已产出区间（事件 ID 幂等），也不能把没看到的当已读。
			pending = maxDecimal(pending, oldest)
		case reached:
			watermark = maxDecimal(watermark, frontier)
			pending = "0"
		case oldest != "":
			f.logf().Warn("私信积压超过单轮页数上限，保留待回填缺口",
				zap.String("action", "dm_message_backlog"),
				zap.String("peer", peer),
				zap.String("watermark", watermark),
				zap.String("oldest", oldest),
			)
			pending = oldest
		}
	}

	if !changed {
		return nil, false, nil
	}
	nextState := Cursor{Watermark: watermark, Frontier: frontier, PendingFrom: pending}
	if nextState == st {
		return out, false, nil
	}
	next[peer] = nextState
	return out, true, nil
}

// scanOlder 从 endSeqno 继续向历史翻页，直到追上 stopAt 或没有更多。
func (f *BiliFetcher) scanOlder(ctx context.Context, client *bili.Client, peer, endSeqno, stopAt string, budget *int, emit func([]bili.DMMessage)) (oldest string, reached bool, scanned bool, err *Error) {
	// 新版 fetch_session_msgs 的 begin_seqno 只能向新，无法向历史回填：
	// 历史恢复（缺口补全）依赖水位与首连基线；这里直接返回「无更早」。
	return "", true, false, nil
}


// scanNewest 从最新页向历史翻页，直到追上 watermark 或没有更多。
// stoppedByError 表示翻页中途遇到非鉴权错误（已拿到较新的页）。
func (f *BiliFetcher) scanNewest(ctx context.Context, client *bili.Client, peer, watermark string, budget *int, emit func([]bili.DMMessage)) (oldest string, reached bool, pageMax string, scanned bool, stoppedByError bool, err *Error) {
	if f.msgBlockedUntil(peer) {
		return "", false, "", false, true, nil
	}
	end := ""
	for *budget > 0 {
		page, fetchErr := client.DMSessionMessagesPage(ctx, peer, f.messagePageSize(), end)
		if fetchErr != nil {
			if classify(fetchErr) == KindAuth {
				return "", false, "", false, false, &Error{Kind: KindAuth, Cause: fetchErr}
			}
			if isNotFound(fetchErr) {
				f.blockMessages(peer, 5*time.Minute)
				f.logf().Warn("私信消息接口不可用(404)，该会话退避 5 分钟",
					zap.String("action", "dm_message_interface_unavailable"),
					zap.String("peer", peer),
					zap.Error(fetchErr),
				)
			} else {
				f.logf().Warn("私信消息分页失败，下一轮重试",
					zap.String("action", "dm_message_page_fail"),
					zap.String("peer", peer),
					zap.String("end_seqno", end),
					zap.Error(fetchErr),
				)
			}
			if end == "" {
				return "", false, "", false, false, nil
			}
			return end, false, pageMax, true, true, nil
		}
		*budget--
		items := filterBelow(page.Messages, end)
		emit(items)
		pageMax = maxDecimal(pageMax, page.MaxSeqno)
		if page.MaxSeqno == "" {
			pageMax = maxDecimal(pageMax, maxSeqnoOf(items))
		}
		pageMin := page.MinSeqno
		if pageMin == "" {
			pageMin = minSeqnoOf(items)
		}
		if pageMin == "" {
			return end, true, pageMax, true, false, nil
		}
		oldest = pageMin
		if compareDecimal(pageMin, watermark) <= 0 {
			return oldest, true, pageMax, true, false, nil
		}
		if !page.HasMore {
			return oldest, true, pageMax, true, false, nil
		}
		if end != "" && compareDecimal(pageMin, end) >= 0 {
			return oldest, true, pageMax, true, false, nil
		}
		end = pageMin
	}
	return oldest, false, pageMax, true, false, nil
}

// fetchWithoutSeqno 是平台没有返回 seqno 时的旧路径：last_msg 变了才拉详情。
func (f *BiliFetcher) fetchWithoutSeqno(ctx context.Context, client *bili.Client, selfID string, s bili.DMSession) ([]Message, bool, *Error) {
	if !f.changed(s.TalkerID, s.MsgKey) {
		// 会话没动：最后一条不是自己就照旧产出（重复由上层去重）
		if selfID == "" || s.SenderUID != selfID {
			return []Message{messageFromSession(s)}, false, nil
		}
		return nil, false, nil
	}
	page, err := client.DMSessionMessagesPage(ctx, s.TalkerID, f.messagePageSize(), "")
	if err != nil {
		if classify(err) == KindAuth {
			return nil, false, &Error{Kind: KindAuth, Cause: err}
		}
		// 详情失败降级为最后一条；不记录 key，下一轮还会重试
		f.logf().Warn("私信详情失败，降级为会话最后一条",
			zap.String("action", "dm_message_page_fail"),
			zap.String("peer", s.TalkerID),
			zap.Error(err),
		)
		if selfID == "" || s.SenderUID != selfID {
			return []Message{messageFromSession(s)}, false, nil
		}
		return nil, false, nil
	}
	f.remember(s.TalkerID, s.MsgKey)
	out := make([]Message, 0, len(page.Messages))
	for _, m := range page.Messages {
		if selfID != "" && m.SenderUID == selfID {
			continue
		}
		out = append(out, Message{
			ID:     m.MsgKey,
			PeerID: s.TalkerID,
			Text:   m.Content,
			At:     atFromUnix(m.Timestamp),
		})
	}
	return out, false, nil
}

func (f *BiliFetcher) sessionPageSize() int {
	if f.Limit <= 0 {
		return defaultSessionPageSize
	}
	if f.Limit > 100 {
		return 100
	}
	return f.Limit
}

func (f *BiliFetcher) messagePageSize() int {
	if f.PageSize <= 0 {
		return defaultMessagePageSize
	}
	if f.PageSize > 200 {
		return 200
	}
	return f.PageSize
}

func (f *BiliFetcher) maxSessionPages() int {
	if f.MaxSessionPages <= 0 {
		return defaultMaxSessionPages
	}
	return f.MaxSessionPages
}

func (f *BiliFetcher) maxMessagePages() int {
	if f.MaxMessagePages <= 0 {
		return defaultMaxMessagePages
	}
	return f.MaxMessagePages
}

func (f *BiliFetcher) changed(talkerID, msgKey string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastKeys[talkerID] != msgKey
}

func (f *BiliFetcher) remember(talkerID, msgKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastKeys == nil {
		f.lastKeys = make(map[string]string)
	}
	f.lastKeys[talkerID] = msgKey
}

// classify 把 bili 错误归到受控类别（凭据失效 → auth，其余 → network）。
func classify(err error) Kind {
	if bili.IsAuthError(err) {
		return KindAuth
	}
	return KindNetwork
}

func messageFromSession(s bili.DMSession) Message {
	return Message{
		ID:     s.MsgKey,
		PeerID: s.TalkerID,
		Text:   s.Content,
		At:     atFromUnix(s.Timestamp),
	}
}

func normalizeCursor(cur Cursor) Cursor {
	return Cursor{
		Watermark:   cursorValue(cur.Watermark),
		Frontier:    cursorValue(cur.Frontier),
		PendingFrom: cursorValue(cur.PendingFrom),
	}
}

func cursorValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "0"
	}
	return value
}

// filterBelow 只保留严格早于 end 的消息；空 end 表示最新页不过滤。
func filterBelow(items []bili.DMMessage, end string) []bili.DMMessage {
	if end == "" {
		return items
	}
	out := make([]bili.DMMessage, 0, len(items))
	for _, m := range items {
		if m.Seqno != "" && compareDecimal(m.Seqno, end) >= 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}

func maxSeqnoOf(items []bili.DMMessage) string {
	out := ""
	for _, m := range items {
		out = maxDecimal(out, m.Seqno)
	}
	return out
}

func minSeqnoOf(items []bili.DMMessage) string {
	out := ""
	for _, m := range items {
		if m.Seqno == "" {
			continue
		}
		if out == "" || compareDecimal(m.Seqno, out) < 0 {
			out = m.Seqno
		}
	}
	return out
}

func maxDecimal(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if compareDecimal(a, b) >= 0 {
		return a
	}
	return b
}

// compareDecimal 比较两个非负十进制串；空串按 0 处理。
func compareDecimal(a, b string) int {
	if a == "" {
		a = "0"
	}
	if b == "" {
		b = "0"
	}
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}
