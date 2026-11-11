package proactive

// 本文件实现主动弹幕的「声音 + 最近弹幕」旁路采集：
//   - 声音与截帧/识图并发，等待有上限，失败只降级为无声音；
//   - 最近弹幕只读本会话公开入向事件，去掉鱼自己，取最近 10 条；
//   - 本轮感知写进 liveview 缓存，供 ReAct 的 watch_live 主动读取。

import (
	"context"
	"strings"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"go.uber.org/zap"
)

const (
	// DefaultAudioTimeout 是等待一次 30 秒声音采样的上限：
	// 录制 + 抽音频 + 本机 ASR 的正常耗时在 35 秒内，超过就按「没有声音」继续。
	DefaultAudioTimeout = 45 * time.Second
	// chatQueryLimit 多取一倍：过滤鱼自己与空文本后仍够 10 条。
	chatQueryLimit       = 20
	maxLiveChatLines     = 10
	maxLiveChatLineRunes = 80
)

// AudioListener 采一小段直播声音并转写；失败只降级为「没有声音」。
type AudioListener interface {
	Listen(ctx context.Context) (string, error)
}

// ChatSource 读取本会话最近的入向事件（按时间倒序）；EventRepo 直接满足。
type ChatSource interface {
	RecentEvents(ctx context.Context, scope memory.Scope, limit int) ([]event.Event, error)
}

// SenseCache 保存本轮感知；*liveview.Senses 直接满足。
type SenseCache interface {
	UpdateFrame(text string, at time.Time)
	UpdateAudio(text string, at time.Time)
	UpdateChat(lines []string, at time.Time)
}

// startAudio 在独立 goroutine 里采样；返回的 channel 一定只写一次。
func (r *Runner) startAudio(ctx context.Context) <-chan string {
	if r.audio == nil {
		return nil
	}
	ch := make(chan string, 1)
	go func() {
		text, err := r.audio.Listen(ctx)
		if err != nil {
			if ctx.Err() == nil {
				r.log.Warn("主动弹幕声音采样失败，本轮只用画面/弹幕",
					zap.String("action", "proactive_audio"),
					zap.Error(err),
				)
			}
			ch <- ""
			return
		}
		ch <- strings.TrimSpace(text)
	}()
	return ch
}

// waitAudio 等待采样完成；超时或父上下文取消时取消录音并降级为无声音。
func (r *Runner) waitAudio(ctx context.Context, ch <-chan string, cancel context.CancelFunc) string {
	if ch == nil {
		return ""
	}
	timer := time.NewTimer(r.audioTimeout)
	defer timer.Stop()
	select {
	case text := <-ch:
		return strings.TrimSpace(text)
	case <-timer.C:
		cancel()
		r.log.Warn("主动弹幕声音采样超时，本轮只用画面/弹幕",
			zap.String("action", "proactive_audio"),
			zap.Duration("timeout", r.audioTimeout),
		)
		return ""
	case <-ctx.Done():
		cancel()
		return ""
	}
}

// recentChat 读取最近观众弹幕并整理成正序文本行；失败只降级为空。
func (r *Runner) recentChat(ctx context.Context) []string {
	if r.chat == nil {
		return nil
	}
	scope := memory.Scope{Channel: event.ChLive, Session: r.room}
	rows, err := r.chat.RecentEvents(ctx, scope, chatQueryLimit)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Warn("主动弹幕最近弹幕读取失败，本轮不注入",
				zap.String("action", "proactive_chat"),
				zap.Error(err),
			)
		}
		return nil
	}
	lines := make([]string, 0, maxLiveChatLines)
	for _, ev := range rows { // 倒序：先取最近的
		if r.selfID != "" && ev.FromID == r.selfID {
			continue
		}
		text := strings.TrimSpace(ev.Text)
		if text == "" {
			continue
		}
		line := text
		if name := strings.TrimSpace(ev.FromName); name != "" {
			line = name + "：" + text
		}
		lines = append(lines, clipRunes(line, maxLiveChatLineRunes))
		if len(lines) >= maxLiveChatLines {
			break
		}
	}
	// 接口按时间倒序，注入按正序读起来更自然。
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

// updateSenses 把本轮感知写进缓存；ASR 失败传空串，会清掉旧声音。
func (r *Runner) updateSenses(visual, audio string, chat []string, at time.Time) {
	if r.senses == nil {
		return
	}
	r.senses.UpdateFrame(visual, at)
	r.senses.UpdateAudio(audio, at)
	r.senses.UpdateChat(chat, at)
}

func clipRunes(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
