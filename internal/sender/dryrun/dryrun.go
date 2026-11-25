// Package dryrun 提供「只记账、不触达平台」的发送器，用于 staging 压测。
//
// 行为与真实发送器在契约上一致：校验入参、返回稳定回执、标记 Verified；
// 区别只有一处——不产生任何网络请求或子进程。这样压测能覆盖调度、生成、
// 投递计划与 send_log 记账，又不会误发到真实观众。
package dryrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
)

// Sender 是 dry-run 发送器；并发安全。
type Sender struct {
	name string
	seq  atomic.Uint64
	mu   sync.Mutex
	// byChannel 只用于观测：每个通道发送了多少条。
	byChannel map[event.Channel]int64
}

// New 构造 dry-run 发送器；name 只用于日志/指标区分。
func New(name string) *Sender {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "dryrun"
	}
	return &Sender{name: name, byChannel: make(map[event.Channel]int64)}
}

// Name 实现 sender.Sender。
func (s *Sender) Name() string { return s.name }

// Stats 是一次 dry-run 的累计计数。
type Stats struct {
	Sent      int64
	ByChannel map[string]int64
}

// Stats 返回累计发送数；空表也返回非 nil map。
func (s *Sender) Stats() Stats {
	if s == nil {
		return Stats{ByChannel: map[string]int64{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Stats{Sent: int64(s.seq.Load()), ByChannel: make(map[string]int64, len(s.byChannel))}
	for ch, n := range s.byChannel {
		out.ByChannel[string(ch)] = n
	}
	return out
}

// Send 校验后直接返回成功，不发任何请求。
func (s *Sender) Send(ctx context.Context, c event.Claim, p sender.Reply) (sender.Result, error) {
	if s == nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("dry-run 发送器未初始化")}
	}
	if ctx == nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("上下文不能为 nil")}
	}
	if err := ctx.Err(); err != nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindRetry, Cause: err}
	}
	if strings.TrimSpace(c.Event.ID) == "" || c.Lease.Owner == "" || c.Lease.Version < 1 {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("dry-run 需要有效租约")}
	}
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("dry-run 回复正文不能为空")}
	}
	if p.Scope.Channel != c.Event.Channel || p.Scope.Session != c.Event.Session || p.Scope.Private != c.Event.Private {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("dry-run 回复范围与事件不一致")}
	}
	n := s.seq.Add(1)
	s.mu.Lock()
	s.byChannel[c.Event.Channel]++
	s.mu.Unlock()
	return sender.Result{OK: true, MsgID: fmt.Sprintf("dry-%d", n), Verified: true}, nil
}

// Resume 与 Send 语义一致：dry-run 没有「未知」状态。
func (s *Sender) Resume(ctx context.Context, c event.Claim) (sender.Result, error) {
	if s == nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("dry-run 发送器未初始化")}
	}
	if ctx == nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("上下文不能为 nil")}
	}
	if err := ctx.Err(); err != nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindRetry, Cause: err}
	}
	if strings.TrimSpace(c.Event.ID) == "" {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("dry-run 缺少事件 ID")}
	}
	return sender.Result{OK: true, MsgID: fmt.Sprintf("dry-%d", s.seq.Load()), Verified: true}, nil
}
