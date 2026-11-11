package main

// 评论附图：Meta.pics → 下载 → 视觉模型转述/OCR → 注入 ContextInput.Visual。
//
// 边界：
//   - 只拉 https + B站图床域名，限制单图大小与整条评论的总耗时（防 SSRF、防止拖垮回复）；
//   - 任何一张图失败只丢这张图，全部失败等于「没有图片描述」，绝不阻塞评论回复；
//   - 只负责生产上下文，是否/如何回复仍由 Brain/ReAct 决定。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	collectorcomment "RaiseMyFish/internal/collector/comment"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/watch"
	"go.uber.org/zap"
)

const (
	// commentImageMaxBytes 与 watch 读帧上限一致，超过就不送视觉模型。
	commentImageMaxBytes = 4 << 20
	// commentVisionMaxRunes 是注入 ContextInput.Visual 的总长上限（Brain 硬上限 2000）。
	commentVisionMaxRunes = 1500
	// commentImageDescRunes 是单张图描述上限，避免一张长描述挤掉其他图。
	commentImageDescRunes = 500
	commentVisionPrompt   = "这是B站评论里附带的图片。请用中文客观描述画面内容；画面里有文字就逐字转写（OCR），不要猜测或补充没看到的内容。200字以内。"
)

// imageDescriber 是「多帧 + 自定义提示词」的视觉接口；watch.OpenAIDescriber 满足。
type imageDescriber interface {
	Describe(ctx context.Context, frames []string, prompt string) (string, error)
}

// commentVision 负责把一条评论的附图变成一段可注入的文本。
type commentVision struct {
	describer imageDescriber
	client    *http.Client
	timeout   time.Duration
	maxImages int
	log       *zap.Logger
}

// newCommentVision 校验静态参数；timeout 是整条评论（所有图）的总时限。
func newCommentVision(describer imageDescriber, timeout time.Duration, maxImages int, log *zap.Logger) (*commentVision, error) {
	if describer == nil {
		return nil, errors.New("评论图片识别缺少视觉模型")
	}
	if timeout <= 0 {
		return nil, errors.New("评论图片识别超时必须为正")
	}
	if maxImages < 1 || maxImages > 4 {
		return nil, errors.New("评论图片识别张数需为1至4")
	}
	if log == nil {
		log = zap.NewNop()
	}
	vision := &commentVision{
		describer: describer,
		client: &http.Client{
			// 首次 URL 已校验，但重定向也要落回同一白名单，避免借 302 绕到内网。
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("图片重定向次数过多")
				}
				if !allowedCommentImageURL(req.URL.String()) {
					return errors.New("图片重定向目标不在允许范围")
				}
				return nil
			},
		},
		timeout:   timeout,
		maxImages: maxImages,
		log:       log,
	}
	return vision, nil
}

// Describe 返回「评论附图（视觉模型转述）」文本；无图或全部失败返回空串。
func (v *commentVision) Describe(ctx context.Context, ev event.Event) string {
	if v == nil || ev.Channel != event.ChComment {
		return ""
	}
	pics := collectorcomment.Pics(ev.Meta)
	if len(pics) == 0 {
		return ""
	}
	if len(pics) > v.maxImages {
		pics = pics[:v.maxImages]
	}
	// 整条评论共用一份时限：图片多时不能把预算乘以张数。
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	started := time.Now()

	// 保留原图序号：跳过失败图后，描述里的「图2」仍对应评论里的第二张。
	type described struct {
		index int
		text  string
	}
	descriptions := make([]described, 0, len(pics))
	for i, pic := range pics {
		if ctx.Err() != nil {
			break
		}
		desc, err := v.describeOne(ctx, pic)
		if err != nil {
			v.log.Warn("评论图片识别失败，跳过该图",
				zap.String("action", "comment_vision"),
				zap.String("event_id", ev.ID),
				zap.Int("image", i+1),
				zap.Error(err),
			)
			continue
		}
		descriptions = append(descriptions, described{index: i + 1, text: desc})
	}
	if len(descriptions) == 0 {
		v.log.Warn("评论附图全部识别失败，按无图继续",
			zap.String("action", "comment_vision"),
			zap.String("event_id", ev.ID),
			zap.Int("pics", len(pics)),
			zap.Int64("latency_ms", time.Since(started).Milliseconds()),
		)
		return ""
	}

	lines := []string{"评论附图（视觉模型转述）："}
	for _, desc := range descriptions {
		lines = append(lines, fmt.Sprintf("图%d：%s", desc.index, desc.text))
	}
	text := truncateRunes(sanitizeVisionText(strings.Join(lines, "\n")), commentVisionMaxRunes)
	v.log.Info("评论附图识别完成",
		zap.String("action", "comment_vision"),
		zap.String("event_id", ev.ID),
		zap.Int("pics", len(pics)),
		zap.Int("described", len(descriptions)),
		zap.Int("runes", utf8.RuneCountInString(text)),
		zap.Int64("latency_ms", time.Since(started).Milliseconds()),
	)
	return text
}

// describeOne 下载一张图并让视觉模型转述；临时文件用完即删。
func (v *commentVision) describeOne(ctx context.Context, rawURL string) (string, error) {
	path, err := v.fetch(ctx, rawURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(path) }()
	text, err := v.describer.Describe(ctx, []string{path}, commentVisionPrompt)
	if err != nil {
		return "", err
	}
	text = sanitizeVisionText(text)
	if text == "" {
		return "", errors.New("视觉模型返回空描述")
	}
	return truncateRunes(text, commentImageDescRunes), nil
}

// fetch 下载单张图片到临时文件；只接受 https + B站图床域名。
func (v *commentVision) fetch(ctx context.Context, rawURL string) (string, error) {
	if !allowedCommentImageURL(rawURL) {
		return "", errors.New("图片地址不在允许范围")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", errors.New("构造图片请求失败")
	}
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) RaiseMyFish/comment")
	resp, err := v.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("下载评论图片被中止：%w", ctx.Err())
		}
		return "", errors.New("下载评论图片失败")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载评论图片失败：HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, commentImageMaxBytes+1))
	if err != nil {
		return "", errors.New("读取评论图片失败")
	}
	if len(data) > commentImageMaxBytes {
		return "", errors.New("评论图片过大")
	}
	ext := commentImageExt(data)
	if ext == "" {
		return "", errors.New("评论图片格式不支持")
	}
	file, err := os.CreateTemp("", "fish-comment-*"+ext)
	if err != nil {
		return "", errors.New("创建评论图片临时文件失败")
	}
	name := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", errors.New("写入评论图片临时文件失败")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", errors.New("写入评论图片临时文件失败")
	}
	return name, nil
}

// allowedCommentImageURL 只放行 B站图床的 https 地址。
func allowedCommentImageURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{"hdslb.com", "biliimg.com", "bilibili.com"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// commentImageExt 按内容嗅探扩展名；视觉客户端只认 jpg/png/webp。
func commentImageExt(data []byte) string {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

// sanitizeVisionText 清掉 NUL/回车并校验 UTF-8；非法内容返回空串。
func sanitizeVisionText(text string) string {
	if !utf8.ValidString(text) {
		return ""
	}
	text = strings.ReplaceAll(text, "\x00", "")
	text = strings.ReplaceAll(text, "\r", "")
	return strings.TrimSpace(text)
}

// buildCommentVision 按 [comment_vision] 装配；未开启返回 nil（不影响评论回复）。
func buildCommentVision(cfg config.Config, usage store.UsageRepo, log *zap.Logger) (*commentVision, error) {
	if !cfg.CommentVision.Enabled {
		return nil, nil
	}
	if !cfg.LLM.Enabled {
		log.Warn("评论图片识别未装配：LLM 未启用", zap.String("action", "comment_vision_setup"))
		return nil, nil
	}
	_, prov, model, err := cfg.LLM.Resolve(cfg.CommentVision.LLMRoute)
	if err != nil {
		return nil, fmt.Errorf("解析评论图片视觉路由失败：%w", err)
	}
	timeout := time.Duration(cfg.CommentVision.TimeoutMS) * time.Millisecond
	describer, err := watch.NewOpenAIDescriber(prov.BaseURL, prov.APIKey, model, nil, timeout, visionSink(usage, model, prov, log))
	if err != nil {
		return nil, fmt.Errorf("装配评论图片视觉客户端失败：%w", err)
	}
	vision, err := newCommentVision(describer, timeout, cfg.CommentVision.MaxImages, log)
	if err != nil {
		return nil, err
	}
	log.Info("评论图片识别已装配",
		zap.String("action", "comment_vision_setup"),
		zap.String("route", cfg.CommentVision.LLMRoute),
		zap.String("model", model),
		zap.Int("max_images", cfg.CommentVision.MaxImages),
		zap.Int("timeout_ms", cfg.CommentVision.TimeoutMS),
		zap.Bool("usage_recording", usage != nil),
	)
	return vision, nil
}
