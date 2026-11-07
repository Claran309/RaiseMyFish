package dm

// 本文件把 bili 客户端适配成 Transporter / Relation（真实私信发送与互关检查）。

import (
	"context"
	"errors"
	"net/http"

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
	client, err := t.bound(ctx)
	if err != nil {
		return "", err
	}
	msgID, err := client.SendMessage(ctx, mid, text)
	if err != nil {
		if classified := classifyBiliError(err); classified != nil {
			return "", classified
		}
		return "", err
	}
	return msgID, nil
}

// bound 有账号快照时返回固定凭据的客户端，避免请求期切到新活跃账号。
func (t BiliTransporter) bound(ctx context.Context) (*bili.Client, error) {
	actor, ok := bili.ActorFrom(ctx)
	if !ok {
		return t.client, nil
	}
	client, err := t.client.Bind(actor)
	if err != nil {
		return nil, &Error{Kind: KindAuth, Cause: err}
	}
	return client, nil
}

// classifyBiliError 把 B站平台错误映射成受控类别；未知平台错误保持 uncertain。
func classifyBiliError(err error) error {
	var apiErr *bili.Error
	if !errors.As(err, &apiErr) {
		return nil
	}
	switch apiErr.Status {
	case http.StatusPreconditionFailed, http.StatusTooManyRequests:
		return &Error{Kind: KindRetry, Cause: errors.New("私信被平台限流")}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &Error{Kind: KindAuth, Cause: errors.New("私信被权限拒绝")}
	}
	switch apiErr.Code {
	case -101, -111, -403:
		return &Error{Kind: KindAuth, Cause: errors.New("私信被权限拒绝")}
	case -352, -412, -509:
		return &Error{Kind: KindRetry, Cause: errors.New("私信被平台限流")}
	}
	return &Error{Kind: KindUncertain, Cause: errors.New("私信平台错误")}
}

// BiliRelation 用 bili 客户端查关注关系。
type BiliRelation struct{ client *bili.Client }

// NewBiliRelation 构造适配器。
func NewBiliRelation(c *bili.Client) BiliRelation { return BiliRelation{client: c} }

// Attribute 实现 Relation；有账号快照时用同一份凭据查关注关系。
func (r BiliRelation) Attribute(ctx context.Context, mid string) (int, error) {
	if r.client == nil {
		return 0, bili.ErrNoCredential
	}
	if actor, ok := bili.ActorFrom(ctx); ok {
		client, err := r.client.Bind(actor)
		if err != nil {
			return 0, err
		}
		return client.RelationAttribute(ctx, mid)
	}
	return r.client.RelationAttribute(ctx, mid)
}
