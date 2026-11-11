// Package liveview 缓存「主动弹幕本轮看到的直播」：画面、声音、最近弹幕。
//
// 主动弹幕与 ReAct 的 watch_live 工具共用这一份缓存：采样只做一次，
// 单步 Brain 走被动注入，ReAct 通过工具主动读取。缓存带 TTL，
// 过期数据不再冒充「现在」；某一路采集失败时由调用方清空对应字段。
package liveview

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTTL 是感知数据的最长有效期；主动弹幕间隔 80 秒，两轮内仍可复用。
	DefaultTTL = 2 * time.Minute
	// toolBudget 是 watch_live 工具结果的预算；Toolbox 统一按 400 字裁剪。
	toolBudget = 380
	// 各段在上面的总预算里再限长，保证画面/声音/弹幕都能出现。
	maxFrameRunes    = 150
	maxAudioRunes    = 120
	maxChatLines     = 4
	maxChatLineRunes = 40
)

// Options 控制有效期与时钟（测试注入）。
type Options struct {
	TTL time.Duration
	Now func() time.Time
}

// View 是一次感知快照；零值字段表示该路数据不存在或已过期。
type View struct {
	Frame   string
	FrameAt time.Time
	Audio   string
	AudioAt time.Time
	Chat    []string
	ChatAt  time.Time
}

type observed struct {
	text string
	at   time.Time
}

type chatSnapshot struct {
	lines []string
	at    time.Time
}

// Senses 是并发安全的感知缓存：主动弹幕写，ReAct 工具读。
type Senses struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	frame observed
	audio observed
	chat  chatSnapshot
}

// NewSenses 装配感知缓存；零值选项取默认 TTL 与系统时钟。
func NewSenses(opts Options) *Senses {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Senses{ttl: ttl, now: now}
}

// UpdateFrame 记录本轮画面描述；传空串表示清空。
func (s *Senses) UpdateFrame(text string, at time.Time) {
	s.update(func() {
		s.frame = observed{text: strings.TrimSpace(text), at: at}
	})
}

// UpdateAudio 记录本轮 ASR 转写；传空串表示清空（ASR 失败时不能留旧声音）。
func (s *Senses) UpdateAudio(text string, at time.Time) {
	s.update(func() {
		s.audio = observed{text: strings.TrimSpace(text), at: at}
	})
}

// UpdateChat 记录最近观众弹幕（每行一条，已去鱼自己）；传空表示清空。
func (s *Senses) UpdateChat(lines []string, at time.Time) {
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			cleaned = append(cleaned, line)
		}
	}
	s.update(func() {
		s.chat = chatSnapshot{lines: cleaned, at: at}
	})
}

func (s *Senses) update(fn func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

// Snapshot 返回仍在新期内的一路或多路感知。
func (s *Senses) Snapshot() View {
	if s == nil {
		return View{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var view View
	if s.fresh(s.frame.at, now) && s.frame.text != "" {
		view.Frame, view.FrameAt = s.frame.text, s.frame.at
	}
	if s.fresh(s.audio.at, now) && s.audio.text != "" {
		view.Audio, view.AudioAt = s.audio.text, s.audio.at
	}
	if s.fresh(s.chat.at, now) && len(s.chat.lines) > 0 {
		view.Chat = append([]string(nil), s.chat.lines...)
		view.ChatAt = s.chat.at
	}
	return view
}

// fresh 只按有效期判断；未来时间（时钟漂移）按「刚写入」处理。
func (s *Senses) fresh(at, now time.Time) bool {
	return !at.IsZero() && now.Sub(at) <= s.ttl
}

// LiveView 是 ReAct watch_live 工具的实现：把当前感知压成一段紧凑文本。
//
// 总长按 400 字预算留给 Toolbox 裁剪，但这里先按段限长，
// 避免「画面描述太长，声音和弹幕被整段裁掉」。
func (s *Senses) LiveView(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	view := s.Snapshot()
	if view.Frame == "" && view.Audio == "" && len(view.Chat) == 0 {
		return "本轮还没采集到直播内容（画面/声音/弹幕都不可用）", nil
	}
	parts := make([]string, 0, 3)
	if view.Frame != "" {
		parts = append(parts, "画面："+clipRunes(view.Frame, maxFrameRunes))
	}
	if view.Audio != "" {
		parts = append(parts, "主播刚说："+clipRunes(view.Audio, maxAudioRunes))
	}
	if len(view.Chat) > 0 {
		lines := make([]string, 0, maxChatLines)
		for _, line := range view.Chat {
			if len(lines) >= maxChatLines {
				break
			}
			lines = append(lines, "- "+clipRunes(line, maxChatLineRunes))
		}
		parts = append(parts, "最近弹幕：\n"+strings.Join(lines, "\n"))
	}
	return clipRunes(strings.Join(parts, "\n"), toolBudget), nil
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
