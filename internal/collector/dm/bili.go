package dm

// 本文件把 bili 客户端适配成 Fetcher（真实私信会话拉取）。

import (
	"context"
	"time"

	"RaiseMyFish/internal/bili"
)

// atFromUnix 把秒级时间戳转成时间；0 表示未知。
func atFromUnix(ts int64) time.Time {
	if ts <= 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

// NewBiliFetcher 构造适配器；client 为 nil 时 Fetch 返回鉴权错误（上层会停采集）。
func NewBiliFetcher(c *bili.Client, selfID string, limit int) BiliFetcher {
	return BiliFetcher{client: c, SelfID: selfID, Limit: limit}
}

// BiliFetcher 通过 bili 客户端拉会话列表并转成 Message。
type BiliFetcher struct {
	client *bili.Client
	// SelfID 用于过滤「自己发的」消息。
	SelfID string
	// Limit 是单次拉取的会话数（<=0 用 20）。
	Limit int
}

// Fetch 实现 Fetcher。
func (f BiliFetcher) Fetch(ctx context.Context) ([]Message, error) {
	if f.client == nil {
		return nil, &Error{Kind: KindAuth, Cause: bili.ErrNoCredential}
	}
	sessions, err := f.client.DMSessions(ctx, f.Limit)
	if err != nil {
		return nil, &Error{Kind: KindNetwork, Cause: err}
	}
	out := make([]Message, 0, len(sessions))
	for _, s := range sessions {
		if f.SelfID != "" && s.SenderUID == f.SelfID {
			continue // 自己发的，不采集
		}
		out = append(out, Message{
			ID:     s.MsgKey,
			PeerID: s.TalkerID,
			Text:   s.Content,
			At:     atFromUnix(s.Timestamp),
		})
	}
	return out, nil
}
