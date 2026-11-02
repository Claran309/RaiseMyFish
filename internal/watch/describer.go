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

// OpenAIDescriber 以 OpenAI 兼容的 image_url（base64）方式读帧。
// 它不复用 Brain 的纯文本 Provider，避免把视觉语义塞进弹幕请求路径。
type OpenAIDescriber struct {
	endpoint string
	key      string
	model    string
	client   *http.Client
}

// NewOpenAIDescriber 校验端点与模型；密钥只来自环境。
func NewOpenAIDescriber(baseURL, apiKey, model string, client *http.Client, timeout time.Duration) (*OpenAIDescriber, error) {
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
	return &OpenAIDescriber{endpoint: u.String(), key: apiKey, model: model, client: &copied}, nil
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

type visionResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Describe 一次请求携带全部帧；模型返回空文本视为失败，不编造描述。
func (d *OpenAIDescriber) Describe(ctx context.Context, frames []string, prompt string) (string, error) {
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
	if len(decoded.Choices) != 1 || decoded.Choices[0].Message.Content == nil {
		return "", errors.New("视觉模型响应缺少描述")
	}
	text := strings.TrimSpace(*decoded.Choices[0].Message.Content)
	if text == "" {
		return "", errors.New("视觉模型返回空描述")
	}
	return text, nil
}

func readFrame(path string) ([]byte, string, error) {
	var mime string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".png":
		mime = "image/png"
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
