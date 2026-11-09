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
	"os"
	"strconv"
	"strings"
	"sync"
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

// ErrNotLoggedIn 表示接口可达但账号未登录（cookie 失效/过期）。
var ErrNotLoggedIn = errors.New("B站账号未登录")

// Options 是客户端参数。
type Options struct {
	APIRoot  string
	LiveRoot string
	VCRoot   string
	// Cookie 是完整 Cookie 头（需含 SESSDATA；副作用还需 bili_jct）。
	Cookie string
	// CookieFile 是凭据文件路径；Cookie 为空时从它读（对应 FISH_BILI_COOKIE_FILE）。
	CookieFile string
	// Resolver 非空时每次请求都从它取当前凭据，账号切换即时生效；
	// 此时忽略 Cookie/CookieFile。Resolver 返回的错误必须不含凭据。
	Resolver func(ctx context.Context) (cookie, selfMID string, err error)
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
	resolver      func(ctx context.Context) (cookie, selfMID string, err error)
	http          *http.Client

	// WBI 密钥按客户端缓存，1 小时内复用；过期后重新从 nav 获取。
	wbiMu    sync.Mutex
	wbiMixin string
	wbiUntil time.Time
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

// New 构造客户端；无 Resolver 且 cookie 为空时返回 ErrNoCredential（调用方按「未配置」处理）。
func New(o Options) (*Client, error) {
	var cookie string
	if o.Resolver == nil {
		cookie = strings.TrimSpace(o.Cookie)
		if cookie == "" && strings.TrimSpace(o.CookieFile) != "" {
			// 支持 _FILE 形式：读文件内容当 Cookie，失败按未配置处理
			data, err := os.ReadFile(strings.TrimSpace(o.CookieFile))
			if err != nil {
				return nil, ErrNoCredential
			}
			cookie = strings.TrimSpace(string(data))
		}
		if cookie == "" {
			return nil, ErrNoCredential
		}
		if strings.ContainsAny(cookie, "\r\n\x00") {
			return nil, errors.New("B站凭据格式非法")
		}
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
		api:      orDefault(o.APIRoot, defaultAPIRoot),
		live:     orDefault(o.LiveRoot, defaultLiveRoot),
		vc:       orDefault(o.VCRoot, defaultVCRoot),
		cookie:   cookie,
		csrf:     parseCSRF(cookie),
		selfMID:  strings.TrimSpace(o.SelfMID),
		resolver: o.Resolver,
		http:     client,
	}, nil
}

// HasCSRF 表示凭据里带 bili_jct，可执行写操作；
// 动态解析时无法预知，返回 true，由请求期检查兜底。
func (c *Client) HasCSRF() bool { return c != nil && (c.csrf != "" || c.resolver != nil) }

// HasResolver 表示凭据在每次请求时动态解析。
func (c *Client) HasResolver() bool { return c != nil && c.resolver != nil }

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

// credential 是一次请求实际使用的凭据（静态或动态解析所得）。
type credential struct {
	cookie  string
	csrf    string
	selfMID string
}

// resolve 取本次请求的凭据；动态模式下每次请求都重新解析，账号切换即时生效。
func (c *Client) resolve(ctx context.Context) (credential, error) {
	if c == nil {
		return credential{}, ErrNoCredential
	}
	if c.resolver == nil {
		return credential{cookie: c.cookie, csrf: c.csrf, selfMID: c.selfMID}, nil
	}
	cookie, selfMID, err := c.resolver(ctx)
	if err != nil {
		if errors.Is(err, ErrNoCredential) {
			return credential{}, ErrNoCredential
		}
		return credential{}, fmt.Errorf("解析B站凭据失败：%w", err)
	}
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return credential{}, ErrNoCredential
	}
	if strings.ContainsAny(cookie, "\r\n\x00") {
		return credential{}, errors.New("B站凭据格式非法")
	}
	selfMID = strings.TrimSpace(selfMID)
	if selfMID == "" {
		selfMID = c.selfMID
	}
	return credential{cookie: cookie, csrf: parseCSRF(cookie), selfMID: selfMID}, nil
}

// wrapResolveError 保留 ErrNoCredential 哨兵，其余错误包装成可安全记录的调用错误。
func wrapResolveError(op string, err error) error {
	if errors.Is(err, ErrNoCredential) {
		return ErrNoCredential
	}
	return &Error{Op: op, Cause: err}
}

// requireCSRF 检查写操作所需 csrf；缺失时直接拒绝，不发出请求。
func requireCSRF(op, csrf string) error {
	if csrf == "" {
		return &Error{Op: op, Cause: errors.New("凭据缺少bili_jct，无法执行写操作")}
	}
	return nil
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

// UserInfo 是当前凭据对应的账号身份。
type UserInfo struct {
	MID  string
	Name string
}

// Nav 调 get_info（/x/web-interface/nav）确认当前凭据身份；
// 未登录/凭据失效统一返回 ErrNotLoggedIn。
func (c *Client) Nav(ctx context.Context) (UserInfo, error) {
	var data struct {
		IsLogin bool            `json:"isLogin"`
		MID     json.RawMessage `json:"mid"`
		UName   string          `json:"uname"`
	}
	if err := c.do(ctx, "nav", http.MethodGet, c.api+"/x/web-interface/nav", nil, &data); err != nil {
		if IsAuthError(err) {
			return UserInfo{}, ErrNotLoggedIn
		}
		return UserInfo{}, err
	}
	mid := rawJSONID(data.MID)
	if !data.IsLogin || mid == "0" || !isDigits(mid) {
		return UserInfo{}, ErrNotLoggedIn
	}
	return UserInfo{MID: mid, Name: strings.TrimSpace(data.UName)}, nil
}

// IsAuthError 判断错误是否表示凭据失效（未登录/被平台拒绝）。
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotLoggedIn) {
		return true
	}
	var typed *Error
	if !errors.As(err, &typed) {
		return false
	}
	if typed.Status == http.StatusUnauthorized || typed.Status == http.StatusForbidden {
		return true
	}
	return typed.Code == -101 || typed.Code == -111
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
	if !isDigits(mid) {
		return errors.New("mid 必须是纯数字")
	}
	cred, err := c.resolve(ctx)
	if err != nil {
		return wrapResolveError("relation", err)
	}
	if err := requireCSRF("relation", cred.csrf); err != nil {
		return err
	}
	form := url.Values{}
	form.Set("fid", mid)
	form.Set("act", act)
	form.Set("re_src", relationReSrc)
	form.Set("csrf", cred.csrf)
	return c.send(ctx, cred, "relation", http.MethodPost, c.api+"/x/relation/modify", form, nil)
}

// Like 给直播间点赞。
func (c *Client) Like(ctx context.Context, room string, count int) error {
	if !isDigits(room) {
		return errors.New("房间号必须是纯数字")
	}
	if count < 1 || count > 3 {
		return errors.New("点赞次数需为1至3")
	}
	cred, err := c.resolve(ctx)
	if err != nil {
		return wrapResolveError("like", err)
	}
	if err := requireCSRF("like", cred.csrf); err != nil {
		return err
	}
	form := url.Values{}
	form.Set("room_id", room)
	form.Set("count", strconv.Itoa(count))
	form.Set("csrf", cred.csrf)
	return c.send(ctx, cred, "like", http.MethodPost, c.live+"/room/v1/Room/like", form, nil)
}

// SendDM 发私信（只允许由工具层白名单放行后的调用）。
func (c *Client) SendDM(ctx context.Context, mid, text string) error {
	if !isDigits(mid) {
		return errors.New("mid 必须是纯数字")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("私信内容不能为空")
	}
	cred, err := c.resolve(ctx)
	if err != nil {
		return wrapResolveError("dm", err)
	}
	if err := requireCSRF("dm", cred.csrf); err != nil {
		return err
	}
	if cred.selfMID == "" || !isDigits(cred.selfMID) {
		return errors.New("缺少本账号UID，无法发私信")
	}
	content, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return errors.New("私信内容编码失败")
	}
	form := url.Values{}
	form.Set("msg[sender_uid]", cred.selfMID)
	form.Set("msg[receiver_id]", mid)
	form.Set("msg[receiver_type]", strconv.Itoa(receiverTypeUser))
	form.Set("msg[msg_type]", strconv.Itoa(msgTypeText))
	form.Set("msg[content]", string(content))
	form.Set("csrf", cred.csrf)
	form.Set("csrf_token", cred.csrf)
	return c.send(ctx, cred, "dm", http.MethodPost, c.vc+"/web_im/v1/web_im/send_msg", form, nil)
}

// PostDynamic 发一条纯文本动态（默认由工具层开关关掉）。
func (c *Client) PostDynamic(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("动态内容不能为空")
	}
	cred, err := c.resolve(ctx)
	if err != nil {
		return wrapResolveError("dynamic", err)
	}
	if err := requireCSRF("dynamic", cred.csrf); err != nil {
		return err
	}
	form := url.Values{}
	form.Set("dynamic_id", "0")
	form.Set("type", "4")
	form.Set("rid", "0")
	form.Set("content", text)
	form.Set("csrf", cred.csrf)
	form.Set("csrf_token", cred.csrf)
	return c.send(ctx, cred, "dynamic", http.MethodPost, c.vc+"/dynamic_svr/v1/dynamic_svr/create", form, nil)
}

// envelope 是 B站统一响应壳。
//
// Code 保留原始JSON：只用 int 会把「缺失/null/字符串」与真正的 code=0
// 混为同一零值，HTTP200 空对象会被误判成功。
type envelope struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// ParseEnvelopeCode 解析平台响应码，只认 JSON 整数。
// 缺失、null、字符串、浮点等一律无效；调用方必须按未知处理，不能当成功。
func ParseEnvelopeCode(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 32)
	if err != nil {
		return 0, false
	}
	return int(n), true
}

// do 解析当前凭据后发一次请求；永不重试。
func (c *Client) do(ctx context.Context, op, method, endpoint string, form url.Values, out any) error {
	cred, err := c.resolve(ctx)
	if err != nil {
		return wrapResolveError(op, err)
	}
	return c.send(ctx, cred, op, method, endpoint, form, out)
}

// refererFor 按接口来源返回对应 Referer。
//
// B站按 Referer 校验来源：直播接口要求 live.bilibili.com，空间/用户主页接口
// 要求 space.bilibili.com；用错来源会被风控返回 412（实测 space_videos）。
func refererFor(op string) string {
	if strings.HasPrefix(op, "space_") || op == "wbi_keys" {
		return "https://space.bilibili.com/"
	}
	return "https://live.bilibili.com/"
}

// send 用给定凭据发一次请求并解析 code。
func (c *Client) send(ctx context.Context, cred credential, op, method, endpoint string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return &Error{Op: op, Cause: errors.New("构造请求失败")}
	}
	req.Header.Set("Cookie", cred.cookie)
	req.Header.Set("Referer", refererFor(op))
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
	code, ok := ParseEnvelopeCode(env.Code)
	if !ok {
		return &Error{Op: op, Cause: errors.New("响应缺少有效code")}
	}
	if code != 0 {
		return &Error{Op: op, Code: code}
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
