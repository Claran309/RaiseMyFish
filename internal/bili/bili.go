// Package bili 是 ReAct 工具用的 B站只读/副作用接口客户端（带 cookie 与 csrf）。
//
// 边界（风控底线）：
//   - 只负责「发一次请求 + 解析 code」，重试由上层禁止（避免触发风控）；
//   - 日志与错误里绝不出现 cookie/csrf/正文；
//   - 未配置凭据时不构造客户端，让上层留 nil 并回「未配置」，不报错。
package bili

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 默认根地址（测试用 httptest 覆盖）。
const (
	defaultAPIRoot  = "https://api.bilibili.com"
	defaultLiveRoot = "https://api.live.bilibili.com"
	defaultVCRoot   = "https://api.vc.bilibili.com"
	// 关注来源（re_src）固定为 11（个人主页），与网页行为一致。
	relationReSrc = "11"
	// 私信接收方类型：1 = 用户。
	receiverTypeUser = 1
	// 私信消息类型：1 = 文本。
	msgTypeText = 1
)

// ErrNoCredential 表示没有可用 cookie，调用方应保持 nil 而不是报错。
var ErrNoCredential = errors.New("未配置B站凭据")

// Options 是客户端参数。
type Options struct {
	APIRoot  string
	LiveRoot string
	VCRoot   string
	// Cookie 是完整 Cookie 头（需含 SESSDATA；副作用还需 bili_jct）。
	Cookie string
	// SelfMID 是本账号 UID（私信 sender_uid 需要）。
	SelfMID string
	Timeout time.Duration
	Client  *http.Client
}

// Client 同时实现直播间状态、关注、点赞、私信、动态五类调用。
type Client struct {
	api, live, vc string
	cookie, csrf  string
	selfMID       string
	http          *http.Client
}

// Error 是可安全记录的调用错误（不含 cookie/正文）。
type Error struct {
	Op     string
	Code   int
	Status int
	Cause  error
}

func (e *Error) Error() string {
	base := fmt.Sprintf("B站接口%s失败", e.Op)
	switch {
	case e.Status > 0:
		base += fmt.Sprintf("（HTTP %d）", e.Status)
	case e.Code != 0:
		base += fmt.Sprintf("（code %d）", e.Code)
	}
	// Cause 只包含本地固定文案（不携带 cookie/正文），拼上便于排查
	if e.Cause != nil {
		base += "：" + e.Cause.Error()
	}
	return base
}

func (e *Error) Unwrap() error { return e.Cause }

// New 构造客户端；cookie 为空返回 ErrNoCredential（调用方按「未配置」处理）。
func New(o Options) (*Client, error) {
	cookie := strings.TrimSpace(o.Cookie)
	if cookie == "" {
		return nil, ErrNoCredential
	}
	if strings.ContainsAny(cookie, "\r\n\x00") {
		return nil, errors.New("B站凭据格式非法")
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &Client{
		api:     orDefault(o.APIRoot, defaultAPIRoot),
		live:    orDefault(o.LiveRoot, defaultLiveRoot),
		vc:      orDefault(o.VCRoot, defaultVCRoot),
		cookie:  cookie,
		csrf:    parseCSRF(cookie),
		selfMID: strings.TrimSpace(o.SelfMID),
		http:    client,
	}, nil
}

// HasCSRF 表示凭据里带 bili_jct，可执行写操作。
func (c *Client) HasCSRF() bool { return c != nil && c.csrf != "" }

// parseCSRF 从 Cookie 头里取 bili_jct（B站写接口的 csrf 参数就是它）。
func parseCSRF(cookie string) string {
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "bili_jct=") {
			continue
		}
		if v := strings.TrimPrefix(part, "bili_jct="); v != "" {
			return v
		}
	}
	return ""
}

// LiveStatus 查直播间状态，返回一句话摘要。
func (c *Client) LiveStatus(ctx context.Context, room string) (string, error) {
	if !isDigits(room) {
		return "", errors.New("房间号必须是纯数字")
	}
	var data struct {
		LiveStatus int    `json:"live_status"`
		Title      string `json:"title"`
		Online     int    `json:"online"`
	}
	endpoint := c.live + "/room/v1/Room/get_info?room_id=" + url.QueryEscape(room)
	if err := c.do(ctx, "live_status", http.MethodGet, endpoint, nil, &data); err != nil {
		return "", err
	}
	state := "未开播"
	if data.LiveStatus == 1 {
		state = "直播中"
	}
	title := strings.TrimSpace(data.Title)
	if title == "" {
		title = "（无标题）"
	}
	return fmt.Sprintf("%s：%s，人气 %d", state, title, data.Online), nil
}

// Follow 关注用户（act=1）。
func (c *Client) Follow(ctx context.Context, mid string) error {
	return c.relation(ctx, mid, "1")
}

// Unfollow 取关用户（act=2）。护栏（不取关主人/自己）在工具层，不在这里。
func (c *Client) Unfollow(ctx context.Context, mid string) error {
	return c.relation(ctx, mid, "2")
}

func (c *Client) relation(ctx context.Context, mid, act string) error {
	if err := c.writable("relation"); err != nil {
		return err
	}
	if !isDigits(mid) {
		return errors.New("mid 必须是纯数字")
	}
	form := url.Values{}
	form.Set("fid", mid)
	form.Set("act", act)
	form.Set("re_src", relationReSrc)
	form.Set("csrf", c.csrf)
	return c.do(ctx, "relation", http.MethodPost, c.api+"/x/relation/modify", form, nil)
}

// Like 给直播间点赞。
func (c *Client) Like(ctx context.Context, room string, count int) error {
	if err := c.writable("like"); err != nil {
		return err
	}
	if !isDigits(room) {
		return errors.New("房间号必须是纯数字")
	}
	if count < 1 || count > 3 {
		return errors.New("点赞次数需为1至3")
	}
	form := url.Values{}
	form.Set("room_id", room)
	form.Set("count", strconv.Itoa(count))
	form.Set("csrf", c.csrf)
	return c.do(ctx, "like", http.MethodPost, c.live+"/room/v1/Room/like", form, nil)
}

// SendDM 发私信（只允许由工具层白名单放行后的调用）。
func (c *Client) SendDM(ctx context.Context, mid, text string) error {
	if err := c.writable("dm"); err != nil {
		return err
	}
	if !isDigits(mid) {
		return errors.New("mid 必须是纯数字")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("私信内容不能为空")
	}
	if c.selfMID == "" || !isDigits(c.selfMID) {
		return errors.New("缺少本账号UID，无法发私信")
	}
	content, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return errors.New("私信内容编码失败")
	}
	form := url.Values{}
	form.Set("msg[sender_uid]", c.selfMID)
	form.Set("msg[receiver_id]", mid)
	form.Set("msg[receiver_type]", strconv.Itoa(receiverTypeUser))
	form.Set("msg[msg_type]", strconv.Itoa(msgTypeText))
	form.Set("msg[content]", string(content))
	form.Set("csrf", c.csrf)
	form.Set("csrf_token", c.csrf)
	return c.do(ctx, "dm", http.MethodPost, c.vc+"/web_im/v1/web_im/send_msg", form, nil)
}

// PostDynamic 发一条纯文本动态（默认由工具层开关关掉）。
func (c *Client) PostDynamic(ctx context.Context, text string) error {
	if err := c.writable("dynamic"); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("动态内容不能为空")
	}
	form := url.Values{}
	form.Set("dynamic_id", "0")
	form.Set("type", "4")
	form.Set("rid", "0")
	form.Set("content", text)
	form.Set("csrf", c.csrf)
	form.Set("csrf_token", c.csrf)
	return c.do(ctx, "dynamic", http.MethodPost, c.vc+"/dynamic_svr/v1/dynamic_svr/create", form, nil)
}

// writable 检查写操作所需凭据（csrf 缺失时直接拒绝，不打任何凭据）。
func (c *Client) writable(op string) error {
	if c == nil {
		return ErrNoCredential
	}
	if c.csrf == "" {
		return &Error{Op: op, Cause: errors.New("凭据缺少bili_jct，无法执行写操作")}
	}
	return nil
}

// envelope 是 B站统一响应壳。
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// do 发一次请求并解析 code；永不重试。
func (c *Client) do(ctx context.Context, op, method, endpoint string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return &Error{Op: op, Cause: errors.New("构造请求失败")}
	}
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("Referer", "https://live.bilibili.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) RaiseMyFish/0.22")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Op: op, Cause: errors.New("网络请求失败")}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &Error{Op: op, Status: resp.StatusCode}
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &Error{Op: op, Cause: errors.New("读取响应失败")}
	}
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return &Error{Op: op, Cause: errors.New("响应不是合法JSON")}
	}
	if env.Code != 0 {
		return &Error{Op: op, Code: env.Code}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return &Error{Op: op, Cause: errors.New("响应数据格式不符")}
		}
	}
	return nil
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimRight(strings.TrimSpace(v), "/")
}

func isDigits(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
