package bili

// 本文件定义一次平台操作绑定的账号快照。
//
// 背景：动态 Resolver 按请求取当前活跃账号，HTTP 层会即时切号；
// 如果一次采集/发送跨越切号时刻，就会出现「读用新号、归属按旧号」的身份撕裂。
// 调用方必须在操作开始时取一份 Actor，并用 Bind 固定本次操作使用的客户端。
//
// 边界：Cookie/CSRF 只允许留在进程内，日志只能记录 AccountID/MID/Source。

import (
	"context"
	"errors"
	"strings"
)

// Actor 是不可变的账号快照（account_id + self_id + 本次操作的凭据）。
type Actor struct {
	// AccountID 是内部账号 id（DB 账号为 account-xxx，环境兜底为 env-<mid>）。
	AccountID string
	// MID 是 B站 UID（平台身份）。
	MID string
	// Cookie/CSRF 只供本次操作使用，禁止写日志或落库。
	Cookie string
	CSRF   string
	// Source 是凭据来源（db/env），用于诊断。
	Source string
}

// Valid 判断快照是否至少有一份可用的读凭据。
func (a Actor) Valid() bool {
	if strings.TrimSpace(a.Cookie) == "" {
		return false
	}
	return !strings.ContainsAny(a.Cookie, "\r\n\x00")
}

// Identified 判断快照是否带有可归属的平台身份（写操作必须满足）。
func (a Actor) Identified() bool {
	return a.Valid() && isDigits(strings.TrimSpace(a.MID))
}

// Snapshot 从客户端当前凭据生成快照；动态客户端在这里冻结本次操作的账号。
func (c *Client) Snapshot(ctx context.Context) (Actor, error) {
	cred, err := c.resolve(ctx)
	if err != nil {
		return Actor{}, err
	}
	return Actor{
		AccountID: cred.selfMID,
		MID:       cred.selfMID,
		Cookie:    cred.cookie,
		CSRF:      cred.csrf,
	}, nil
}

// Bind 返回固定使用该快照的新客户端：不带 Resolver，后续请求不会切号。
func (c *Client) Bind(a Actor) (*Client, error) {
	if c == nil {
		return nil, ErrNoCredential
	}
	if !a.Valid() {
		return nil, errors.New("B站账号快照无效")
	}
	csrf := strings.TrimSpace(a.CSRF)
	if csrf == "" {
		csrf = parseCSRF(a.Cookie)
	}
	return &Client{
		api:     c.api,
		live:    c.live,
		vc:      c.vc,
		cookie:  strings.TrimSpace(a.Cookie),
		csrf:    csrf,
		selfMID: strings.TrimSpace(a.MID),
		http:    c.http,
	}, nil
}

type actorKey struct{}

// WithActor 把账号快照放进上下文，供内层发送器使用同一份凭据。
func WithActor(ctx context.Context, a Actor) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom 读取本次操作绑定的账号快照。
func ActorFrom(ctx context.Context) (Actor, bool) {
	if ctx == nil {
		return Actor{}, false
	}
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}
