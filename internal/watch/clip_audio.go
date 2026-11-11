package watch

// 本文件把「录一段直播 → 抽音频 → ASR」串成主动弹幕可调用的声音采样。
//
// 边界：
//   - 一次采样只服务一轮主动弹幕，调用方负责并发启动并等待；
//   - 临时视频/音频无论成功失败都清理，不把磁盘留给下一轮；
//   - 无音轨返回 ErrNoAudio（告警），其他失败带阶段包装，由调用方降级。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// DefaultClipAudioSeconds 与派发单一致：主动弹幕抽最近 30 秒音频。
	DefaultClipAudioSeconds = 30
	// clipAudioMaxTimeout 是单次采样的硬上限（录制 + 抽音频 + 转写）。
	clipAudioMaxTimeout = 2 * time.Minute
)

// ClipAudio 采样一段直播音频并转写成文本。
type ClipAudio struct {
	recorder    Recorder
	extractor   AudioExtractor
	transcriber Transcriber
	seconds     int
}

// NewClipAudio 校验依赖与秒数；seconds 为 0 时取 DefaultClipAudioSeconds。
func NewClipAudio(recorder Recorder, extractor AudioExtractor, transcriber Transcriber, seconds int) (*ClipAudio, error) {
	if recorder == nil || extractor == nil || transcriber == nil {
		return nil, errors.New("声音采样缺少录制/抽音频/转写环节")
	}
	if seconds == 0 {
		seconds = DefaultClipAudioSeconds
	}
	if seconds < 1 || seconds > 120 {
		return nil, errors.New("声音采样秒数必须在1至120之间")
	}
	return &ClipAudio{recorder: recorder, extractor: extractor, transcriber: transcriber, seconds: seconds}, nil
}

// Listen 录制并转写一段音频；返回的是服务端真实转写，空结果显式报错。
//
// 调用方通常把它放在独立 goroutine 里与截帧/识图并发执行，
// 再用自己的超时控制等待时间（ReAct 只有 8 秒，不能在这里无限等）。
func (c *ClipAudio) Listen(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", errors.New("声音采样缺少上下文")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	listenCtx, cancel := context.WithTimeout(ctx, clipAudioMaxTimeout)
	defer cancel()

	video, err := c.recorder.Record(listenCtx, c.seconds)
	if err != nil {
		return "", fmt.Errorf("录制直播音频失败：%w", err)
	}
	defer func() { _ = os.Remove(video) }()

	audio, err := c.extractor.Extract(listenCtx, video)
	if err != nil {
		// ErrNoAudio 是「直播没有音轨」的告警，保持原样供调用方识别。
		if errors.Is(err, ErrNoAudio) {
			return "", err
		}
		return "", fmt.Errorf("抽取直播音频失败：%w", err)
	}
	defer func() { _ = os.Remove(audio) }()

	text, err := c.transcriber.Transcribe(listenCtx, audio)
	if err != nil {
		return "", fmt.Errorf("转写直播声音失败：%w", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("转写直播声音为空")
	}
	return text, nil
}
