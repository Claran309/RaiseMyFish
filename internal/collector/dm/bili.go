package dm

// 本文件把 bili 客户端适配成 Fetcher（真实私信会话拉取）。
//
// 会话列表只带最后一条消息，连续发多条时前几条会漏；这里对「最后一条变化」
// 的会话再拉一次详情（get_session_msgs），把最近一批消息全部转成 Message。

import (
	"context"
	"sync"
	"time"

	"RaiseMyFish/internal/bili"
)

// defaultDetailLimit 是单会话详情拉取条数（平台上限 100）。
const defaultDetailLimit = 50

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
	}
}

// BiliFetcher 通过 bili 客户端拉会话列表，并对变化会话补拉详情。
type BiliFetcher struct {
	client *bili.Client
	// SelfID 用于过滤「自己发的」消息。
	SelfID string
	// Limit 是单次拉取的会话数（<=0 用 20）。
	Limit int
	// DetailLimit 是单会话详情拉取条数；<=0 用 50。
	DetailLimit int

	mu sync.Mutex
	// lastKeys 记录每个会话上次成功处理的 last_msg key；没变就不再拉详情。
	lastKeys map[string]string
}

// Fetch 实现 Fetcher。
func (f *BiliFetcher) Fetch(ctx context.Context) ([]Message, error) {
	if f.client == nil {
		return nil, &Error{Kind: KindAuth, Cause: bili.ErrNoCredential}
	}
	sessions, err := f.client.DMSessions(ctx, f.Limit)
	if err != nil {
		return nil, &Error{Kind: classify(err), Cause: err}
	}
	out := make([]Message, 0, len(sessions))
	for _, s := range sessions {
		if !f.changed(s.TalkerID, s.MsgKey) {
			// 会话没动：最后一条不是自己就照旧产出（重复由上层去重）
			if f.SelfID == "" || s.SenderUID != f.SelfID {
				out = append(out, messageFromSession(s))
			}
			continue
		}
		msgs, err := f.client.DMSessionMessages(ctx, s.TalkerID, f.detailLimit())
		if err != nil {
			if classify(err) == KindAuth {
				return nil, &Error{Kind: KindAuth, Cause: err}
			}
			// 详情失败降级为最后一条；不记录 key，下一轮还会重试
			if f.SelfID == "" || s.SenderUID != f.SelfID {
				out = append(out, messageFromSession(s))
			}
			continue
		}
		f.remember(s.TalkerID, s.MsgKey)
		for _, m := range msgs {
			if f.SelfID != "" && m.SenderUID == f.SelfID {
				continue
			}
			out = append(out, Message{
				ID:     m.MsgKey,
				PeerID: s.TalkerID,
				Text:   m.Content,
				At:     atFromUnix(m.Timestamp),
			})
		}
	}
	return out, nil
}

func (f *BiliFetcher) detailLimit() int {
	if f.DetailLimit > 0 {
		return f.DetailLimit
	}
	return defaultDetailLimit
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
