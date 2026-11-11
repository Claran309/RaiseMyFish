package watch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxFrameBytes     = 4 << 20
	maxVisionBytes    = 2 << 20
	visionMaxTokens   = 256
	visionTemp        = 0.3
	visionHTTPTimeout = 30 * time.Second
)

// VisionUsage 是一次视觉调用的 token 用量；字段为 nil 表示服务端没给（未知），不是 0。
type VisionUsage struct {
	InputTokens  *int
	OutputTokens *int
}

// UsageSink 记录一次视觉调用；成功与失败都要记（与 Brain/ReAct 口径一致）。
//
// usage 在失败时通常为零值（服务端没回），由装配层写成 nil token；
// elapsed 是本次调用耗时，ok 表示是否拿到有效描述。
type UsageSink func(ctx context.Context, usage VisionUsage, ok bool, elapsed time.Duration)

// OpenAIDescriber 以 OpenAI 兼容的 image_url（base64）方式读帧。
// 它不复用 Brain 的纯文本 Provider，避免把视觉语义塞进弹幕请求路径。
type OpenAIDescriber struct {
	endpoint string
	key      string
	model    string
	client   *http.Client
	// sink 可选：把每次调用的 token 用量写进账本（O08）。
	sink UsageSink
}

// NewOpenAIDescriber 校验端点与模型；密钥只来自环境。
// sink 为 nil 表示不计账（测试或未启用账本）。
func NewOpenAIDescriber(baseURL, apiKey, model string, client *http.Client, timeout time.Duration, sink UsageSink) (*OpenAIDescriber, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.TrimSpace(model) == "" || strings.ContainsAny(apiKey, "\r\n") {
		return nil, errors.New("视觉模型端点或模型配置无效")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, errors.New("视觉模型只允许HTTPS或本机HTTP端点")
	}
	if timeout <= 0 {
		timeout = visionHTTPTimeout
	}
	base := http.DefaultClient
	if client != nil {
		base = client
	}
	copied := *base
	copied.Timeout = timeout
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	return &OpenAIDescriber{endpoint: u.String(), key: apiKey, model: model, client: &copied, sink: sink}, nil
}

type visionPart struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ImageURL *visionLink `json:"image_url,omitempty"`
}

type visionLink struct {
	URL string `json:"url"`
}

type visionRequest struct {
	Model       string      `json:"model"`
	Messages    []visionMsg `json:"messages"`
	MaxTokens   int         `json:"max_tokens"`
	Temperature float64     `json:"temperature"`
	Stream      bool        `json:"stream"`
}

type visionMsg struct {
	Role    string       `json:"role"`
	Content []visionPart `json:"content"`
}

type visionUsage struct {
	// 用指针区分「服务端没给」与「给了 0」：前者记 unknown，后者是真实的 0。
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
}

type visionResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage visionUsage `json:"usage"`
}

// Describe 一次请求携带全部帧；模型返回空文本视为失败，不编造描述。
//
// O08：成功与失败都会把 usage 交给 sink —— 视觉调用以前只解析 choices，
// 费用总览里完全看不到这部分开销。
func (d *OpenAIDescriber) Describe(ctx context.Context, frames []string, prompt string) (text string, err error) {
	start := time.Now()
	var usage VisionUsage
	defer func() {
		if d.sink == nil {
			return
		}
		// ctx 可能已被取消（超时/关闭），记账用它也无妨：装配层会脱离取消再落库。
		d.sink(ctx, usage, err == nil, time.Since(start))
	}()
	if strings.TrimSpace(prompt) == "" || len(frames) == 0 {
		return "", errors.New("读帧缺少提示词或帧")
	}
	parts := make([]visionPart, 0, len(frames)+1)
	parts = append(parts, visionPart{Type: "text", Text: prompt})
	for _, frame := range frames {
		data, mime, err := readFrame(frame)
		if err != nil {
			return "", err
		}
		parts = append(parts, visionPart{
			Type:     "image_url",
			ImageURL: &visionLink{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)},
		})
	}
	payload, err := json.Marshal(visionRequest{
		Model:       d.model,
		Messages:    []visionMsg{{Role: "user", Content: parts}},
		MaxTokens:   visionMaxTokens,
		Temperature: visionTemp,
	})
	if err != nil {
		return "", errors.New("构造视觉模型请求失败")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", errors.New("构造视觉模型请求失败")
	}
	req.Header.Set("Content-Type", "application/json")
	if d.key != "" {
		req.Header.Set("Authorization", "Bearer "+d.key)
	}
	res, err := d.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("视觉模型调用被中止：%w", ctx.Err())
		}
		return "", errors.New("视觉模型网络请求失败")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("视觉模型调用失败：HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxVisionBytes+1))
	if err != nil {
		return "", errors.New("读取视觉模型响应失败")
	}
	if len(body) > maxVisionBytes {
		return "", errors.New("视觉模型响应过大")
	}
	var decoded visionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", errors.New("视觉模型响应格式无法识别")
	}
	// 先记用量：即使后面发现 choices 异常，这次调用也已经产生了费用。
	usage = VisionUsage{
		InputTokens:  positiveTokens(decoded.Usage.PromptTokens),
		OutputTokens: positiveTokens(decoded.Usage.CompletionTokens),
	}
	if len(decoded.Choices) != 1 || decoded.Choices[0].Message.Content == nil {
		return "", errors.New("视觉模型响应缺少描述")
	}
	text = strings.TrimSpace(*decoded.Choices[0].Message.Content)
	if text == "" {
		return "", errors.New("视觉模型返回空描述")
	}
	return text, nil
}

// positiveTokens 把负数/越界 token 当作未知（nil），不让脏数据进账本。
func positiveTokens(n *int) *int {
	if n == nil || *n < 0 || int64(*n) > 2147483647 {
		return nil
	}
	return n
}

func readFrame(path string) ([]byte, string, error) {
	var mime string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".png":
		mime = "image/png"
	case ".webp":
		// 评论图床常见 webp；data URI 直接带 image/webp 即可。
		mime = "image/webp"
	default:
		return nil, "", fmt.Errorf("不支持的帧格式：%s", filepath.Ext(path))
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return nil, "", fmt.Errorf("读取帧失败：%s", filepath.Base(path))
	}
	if info.Size() > maxFrameBytes {
		return nil, "", fmt.Errorf("帧文件过大：%s", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("读取帧失败：%s", filepath.Base(path))
	}
	return data, mime, nil
}
