package web

// 写接口的跨站防护（O04）。
//
// 背景：面板只监听回环地址，但浏览器里的任意网页都能向 127.0.0.1:8090 发请求——
// 只要不带 Origin 校验，一个恶意页面就能删除记忆、切换账号、改运行时配置。
//
// 三层防线，只作用于写方法（GET/HEAD 不拦截：跨站读不到响应体，且读接口无副作用）：
//  1. Host 必须是面板自己的回环地址（防 DNS rebinding 把攻击域名解析到 127.0.0.1）；
//  2. 带 Origin 的请求必须与面板同源（浏览器跨站写会带 Origin，无法伪造）；
//  3. 不带 Origin 的请求视为本地 CLI，必须出示面板令牌（X-Panel-Token 或 Bearer）。
//     缺头本身不等于攻击：老客户端与 curl 都不会带 Origin，给令牌就能过。
//
// 另外写请求体必须是 application/json：text/plain 属于「简单请求」，
// 浏览器不会预检，正是跨站表单能打进来的形态。

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// panelTokenHeader 是本地客户端出示令牌的请求头。
const panelTokenHeader = "X-Panel-Token"

// pageTokenPlaceholder 是静态页里的令牌占位符，由 handlePage 替换成真实令牌。
const pageTokenPlaceholder = "__PANEL_TOKEN__"

// errPanelToken 表示无法生成令牌（熵源异常）。
var errPanelToken = errors.New("生成面板令牌失败")

// newPanelToken 生成本进程的面板令牌（32 字节随机，hex 编码）。
//
// 令牌每次启动重新生成、不落盘：页面从 HTML 里读取，CLI 用 GET /api/token 取。
// 跨站页面拿不到响应体（同源策略且不返回 CORS 头），因此令牌不会被第三方读走。
func newPanelToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", errPanelToken
	}
	return hex.EncodeToString(buf), nil
}

// Token 返回面板令牌，供装配层打印给运维（测试也用它构造请求）。
func (s *Server) Token() string {
	if s == nil {
		return ""
	}
	return s.token
}

// isWriteMethod 判断是否需要跨站防护。
func isWriteMethod(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

// guard 是所有面板路由的统一入口：先校验 Host，再按方法决定是否还要过来源/Content-Type。
//
// O04+ ：读路径也校验 Host。原因是页面 HTML 里带着写接口令牌——
// 如果 DNS rebinding 能让攻击者读到 `/`，他就能拿到令牌绕过写防护，
// 所以「只拦写」等于没堵住 rebinding。读请求不再要求 Origin/令牌（同源策略本就拦读取）。
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1) Host：读写都要过（防 DNS rebinding）。
		if !s.hostAllowed(r.Host) {
			writeError(w, http.StatusForbidden, "请求 Host 不在允许范围（只接受本机回环地址）")
			return
		}
		if !isWriteMethod(r.Method) {
			next(w, r)
			return
		}
		// 2) 来源：带 Origin 必须同源；不带 Origin 必须出示令牌。
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
			if !s.originAllowed(origin) {
				writeError(w, http.StatusForbidden, "跨站请求被拒绝（Origin 与面板不同源）")
				return
			}
		} else if !s.tokenValid(r) {
			writeError(w, http.StatusUnauthorized, "缺少面板令牌（本地 CLI 请带 X-Panel-Token，页面从 /api/token 获取）")
			return
		}
		// 3) Content-Type：有请求体时必须是 JSON。
		if !jsonContentType(r) {
			writeError(w, http.StatusUnsupportedMediaType, "写接口只接受 application/json")
			return
		}
		next(w, r)
	}
}

// hostAllowed 判断 Host 是否属于面板本身：回环主机 + 面板端口。
//
// 端口必须匹配，否则「攻击者用 localhost:其他端口」也能过。
func (s *Server) hostAllowed(hostport string) bool {
	hostport = strings.ToLower(strings.TrimSpace(hostport))
	if hostport == "" {
		return false
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// 不带端口：按主机名判断（默认端口场景）
		return isLoopbackHost(hostport)
	}
	if port != s.port {
		return false
	}
	return isLoopbackHost(host)
}

// originAllowed 判断 Origin 是否与面板同源。
//
// 浏览器跨站写一定会带 Origin 且无法伪造，所以这里只做「主机+端口」比对；
// 协议不强制 https（面板只跑本机 http）。
func (s *Server) originAllowed(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return s.hostAllowed(parsed.Host)
}

// isLoopbackHost 判断主机名是否是回环（localhost / 127.0.0.1 / ::1）。
func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// tokenValid 校验本地客户端令牌（常量时间比较，避免计时侧信道）。
func (s *Server) tokenValid(r *http.Request) bool {
	given := strings.TrimSpace(r.Header.Get(panelTokenHeader))
	if given == "" {
		// 兼容 Bearer：curl -H "Authorization: Bearer <token>"
		if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(auth, "Bearer ") {
			given = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
	}
	if given == "" || s.token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) == 1
}

// jsonContentType 校验写请求体类型：没有请求体时不要求（DELETE/PUT 常无体）。
func jsonContentType(r *http.Request) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	return mediaType == "application/json"
}
