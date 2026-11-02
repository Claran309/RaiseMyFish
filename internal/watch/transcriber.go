package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	asrField         = "file"
	asrModelField    = "model"
	asrLanguageField = "language"
	asrFormatField   = "response_format"
	asrDefaultModel  = "whisper-small"
	asrDefaultLang   = "zh"
	asrDefaultFormat = "json"
	asrMaxBytes      = 2 << 20
	asrHTTPTimeout   = 30 * time.Second
)

// HTTPTranscriber 调本机 OpenAI 兼容 ASR 接口（/v1/audio/transcriptions）：
// multipart 必填 file，可选 model/language/response_format；响应 JSON {"text":...}。
type HTTPTranscriber struct {
	endpoint string
	client   *http.Client
}

// NewHTTPTranscriber 校验 ASR 地址与超时。
func NewHTTPTranscriber(endpoint string, client *http.Client, timeout time.Duration) (*HTTPTranscriber, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("ASR 地址无效")
	}
	if timeout <= 0 {
		timeout = asrHTTPTimeout
	}
	base := http.DefaultClient
	if client != nil {
		base = client
	}
	copied := *base
	copied.Timeout = timeout
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPTranscriber{endpoint: endpoint, client: &copied}, nil
}

// Transcribe 返回服务端真实转写；不可用或空结果都显式报错。
func (t *HTTPTranscriber) Transcribe(ctx context.Context, audioPath string) (string, error) {
	info, err := os.Stat(audioPath)
	if err != nil || info.Size() == 0 {
		return "", errors.New("转写缺少音频文件")
	}
	file, err := os.Open(audioPath)
	if err != nil {
		return "", errors.New("打开音频文件失败")
	}
	defer func() { _ = file.Close() }()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(asrField, filepath.Base(audioPath))
	if err != nil {
		return "", errors.New("构造ASR请求失败")
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", errors.New("读取音频文件失败")
	}
	// 可选字段显式发送：服务端缺省值与本地约定一致，避免依赖实现默认。
	for name, value := range map[string]string{
		asrModelField:    asrDefaultModel,
		asrLanguageField: asrDefaultLang,
		asrFormatField:   asrDefaultFormat,
	} {
		if err := writer.WriteField(name, value); err != nil {
			return "", errors.New("构造ASR请求失败")
		}
	}
	if err := writer.Close(); err != nil {
		return "", errors.New("构造ASR请求失败")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, &body)
	if err != nil {
		return "", errors.New("构造ASR请求失败")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("ASR调用被中止：%w", ctx.Err())
		}
		return "", errors.New("ASR服务不可用")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("ASR调用失败：HTTP %d", res.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(res.Body, asrMaxBytes+1))
	if err != nil {
		return "", errors.New("读取ASR响应失败")
	}
	if len(payload) > asrMaxBytes {
		return "", errors.New("ASR响应过大")
	}
	var decoded struct {
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", errors.New("ASR响应格式无法识别")
	}
	text := ""
	if decoded.Text != nil {
		text = *decoded.Text
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("ASR返回空转写")
	}
	return text, nil
}
