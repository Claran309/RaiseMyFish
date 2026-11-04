package dm

// 本文件把 bili 客户端适配成 Transporter / Relation（真实私信发送与互关检查）。

import (
	"context"

	"RaiseMyFish/internal/bili"
)

// BiliTransporter 用 bili 客户端发私信。
type BiliTransporter struct{ client *bili.Client }

// NewBiliTransporter 构造适配器；client 为 nil 时所有调用返回未配置。
func NewBiliTransporter(c *bili.Client) BiliTransporter { return BiliTransporter{client: c} }

// SendDM 实现 Transporter。
func (t BiliTransporter) SendDM(ctx context.Context, mid, text string) (string, error) {
	if t.client == nil {
		return "", &Error{Kind: KindAuth, Cause: bili.ErrNoCredential}
	}
	msgID, err := t.client.SendMessage(ctx, mid, text)
	if err != nil {
		return "", err
	}
	return msgID, nil
}

// BiliRelation 用 bili 客户端查关注关系。
type BiliRelation struct{ client *bili.Client }

// NewBiliRelation 构造适配器。
func NewBiliRelation(c *bili.Client) BiliRelation { return BiliRelation{client: c} }

// Attribute 实现 Relation。
func (r BiliRelation) Attribute(ctx context.Context, mid string) (int, error) {
	if r.client == nil {
		return 0, bili.ErrNoCredential
	}
	return r.client.RelationAttribute(ctx, mid)
}
