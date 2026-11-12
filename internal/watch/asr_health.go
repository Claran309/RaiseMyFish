package watch

// ASR 健康探测：只判断「本机转写服务是否活着」，不做转写、不读响应正文。
// 放在 watch 包内，watch 与 proactive 两条声音链路共用同一个端点配置。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// asrHealthTimeout 是启动探测的默认超时；服务没起时必须快速失败，不能拖慢 fishd 启动。
const asrHealthTimeout = 2 * time.Second

// CheckASR 探测 ASR 服务的 /health 端点：2xx 视为可用，其余显式报错。
//
// endpoint 是转写地址（.../v1/audio/transcriptions），这里只取 scheme+host 拼 /health。
// 错误信息包含主机名与底层原因，便于区分「没启动」和「端口被占」，不打印响应正文。
func CheckASR(ctx context.Context, endpoint string, client *http.Client, timeout time.Duration) error {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || u.Host == "" || u.User != nil ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("ASR 地址无效")
	}
	if timeout <= 0 {
		timeout = asrHealthTimeout
	}
	base := http.DefaultClient
	if client != nil {
		base = client
	}
	copied := *base
	copied.Timeout = timeout
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	health := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/health"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, health.String(), nil)
	if err != nil {
		return errors.New("构造ASR健康检查请求失败")
	}
	res, err := copied.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("ASR健康检查被中止：%w", ctx.Err())
		}
		return fmt.Errorf("ASR服务不可达（%s）：%w", u.Host, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("ASR健康检查返回 HTTP %d（%s）", res.StatusCode, u.Host)
	}
	return nil
}

// ASRHost 返回端点主机（含端口），仅供日志标注；非法地址返回空串。
func ASRHost(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u == nil {
		return ""
	}
	return u.Host
}
