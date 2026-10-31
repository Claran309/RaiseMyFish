package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

type fakeProvider struct {
	calls    atomic.Int32
	mu       sync.Mutex
	requests []provider.Request
	call     func(context.Context, provider.Request, int) (provider.Response, error)
}

func (p *fakeProvider) Name() string { return "fake" }
func (p *fakeProvider) Chat(ctx context.Context, r provider.Request) (provider.Response, error) {
	n := int(p.calls.Add(1))
	p.mu.Lock()
	p.requests = append(p.requests, r)
	p.mu.Unlock()
	if p.call != nil {
		return p.call(ctx, r, n)
	}
	in, out := 10, 5
	return provider.Response{Text: strings.Repeat("鱼", 60), Usage: provider.Usage{InputTokens: &in, OutputTokens: &out}}, nil
}
func (p *fakeProvider) last() provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[len(p.requests)-1]
}

type fakeUsage struct {
	mu        sync.Mutex
	rows      []store.Usage
	err       error
	contextOK bool
}

func (r *fakeUsage) RecordUsage(ctx context.Context, u store.Usage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contextOK = ctx.Err() == nil
	r.rows = append(r.rows, u)
	return r.err
}
func (r *fakeUsage) snapshot() []store.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]store.Usage(nil), r.rows...)
}

func fixture() (Task, ContextInput) {
	s := memory.Scope{Channel: event.ChLive, Session: "room", Private: false}
	return Task{ID: "task", EventID: "live:room:1", Channel: event.ChLive, Session: "room"}, ContextInput{
		Event:   event.Event{ID: "live:room:1", Channel: event.ChLive, Dir: event.DirIn, Session: "room", Text: "问题内容", At: time.Now()},
		Persona: persona.Pack{Meta: persona.Meta{Name: "test", Version: "v1", Channels: []string{"live"}}, Soul: "身份", Rules: "规则", Samples: "样本", Contexts: map[string]string{"live": "场景"}},
		Memory:  memory.Context{Scope: s}, Shared: memory.SharedState{Scope: s},
	}
}
func options() Options {
	v := DefaultOptions()
	v.Model = "model"
	v.Backoff = time.Millisecond
	return v
}
func makeBrain(t *testing.T, p *fakeProvider, u *fakeUsage, o Options) *OpenAI {
	t.Helper()
	b, err := New(p, u, o, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReplyAssemblyLengthAndKnownCost(t *testing.T) {
	p, u := &fakeProvider{}, &fakeUsage{}
	o := options()
	input, output := 1., 2.
	o.InputUSDPerMillion = &input
	o.OutputUSDPerMillion = &output
	b := makeBrain(t, p, u, o)
	task, in := fixture()
	in.Event.Meta = json.RawMessage(`{"private_key":"NEVER_INCLUDE"}`)
	reply, err := b.Reply(context.Background(), task, in)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(reply.Text) != 40 || len(reply.Actions) != 0 || b.Name() != "fake:model" {
		t.Fatal("回复长度/动作/模型路由错误")
	}
	r := p.last()
	if len(r.Messages) != 2 || r.Messages[0].Content != in.Persona.SystemPrompt() {
		t.Fatal("静态system顺序错误")
	}
	user := r.Messages[1].Content
	if strings.Index(user, "<context>") >= strings.Index(user, "<event>") || strings.Index(user, "<event>") >= strings.Index(user, "<instructions>") {
		t.Fatal("user段顺序错误")
	}
	if strings.Contains(r.Messages[0].Content, "问题内容") || strings.Contains(user, "NEVER_INCLUDE") {
		t.Fatal("动态状态/平台Meta跨边界")
	}
	rows := u.snapshot()
	if len(rows) != 1 || !rows[0].OK || rows[0].Model != "fake:model" || rows[0].CostUSD == nil || *rows[0].CostUSD != .000020 {
		t.Fatalf("费用/账本错误：%+v", rows)
	}
	in.Event.Text = "另一条问题"
	if _, err := b.Reply(context.Background(), task, in); err != nil {
		t.Fatal(err)
	}
	if p.last().Messages[0].Content != r.Messages[0].Content {
		t.Fatal("正常请求的system不稳定")
	}
}

func TestInputPrivacyRejectsBeforeProvider(t *testing.T) {
	for _, mutate := range []func(*Task, *ContextInput){
		func(t *Task, i *ContextInput) { t.EventID = "other" },
		func(t *Task, i *ContextInput) { i.Memory.Scope.Session = "other" },
		func(t *Task, i *ContextInput) { i.Shared.Scope.Private = true },
		func(t *Task, i *ContextInput) { i.Event.Private = true },
		func(t *Task, i *ContextInput) {
			i.Memory.Records = []memory.Record{{ID: "r", Summary: "私密", Private: true, Channel: event.ChDM, Session: "owner", Importance: .5, Confidence: .8}}
		},
		func(t *Task, i *ContextInput) {
			i.Shared.Facts = []memory.Fact{{Key: "balance", Value: "secret", Private: true, Channel: event.ChDM, Session: "owner", SourceID: "ledger", At: time.Now()}}
		},
		func(t *Task, i *ContextInput) { i.Tools = []Tool{{Name: "gift"}} },
		func(t *Task, i *ContextInput) { i.Event.Text = " " },
		func(t *Task, i *ContextInput) { i.Persona.Soul = " " },
	} {
		task, in := fixture()
		mutate(&task, &in)
		p, u := &fakeProvider{}, &fakeUsage{}
		b := makeBrain(t, p, u, options())
		if _, err := b.Reply(context.Background(), task, in); err == nil {
			t.Fatal("越界输入被接受")
		}
		if p.calls.Load() != 0 || len(u.snapshot()) != 0 {
			t.Fatal("拒绝输入仍调用Provider或虚记用量")
		}
	}
}

func TestRetryBoundariesAndUnknownCost(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  provider.Kind
		retry bool
		empty bool
		want  int
	}{
		{"5xx", provider.KindHTTP, true, false, 3}, {"4xx", provider.KindHTTP, false, false, 1}, {"protocol", provider.KindProtocol, false, false, 1}, {"empty", "", false, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{call: func(context.Context, provider.Request, int) (provider.Response, error) {
				if tc.empty {
					return provider.Response{}, nil
				}
				return provider.Response{}, &provider.Error{Kind: tc.kind, Retryable: tc.retry}
			}}
			u := &fakeUsage{}
			b := makeBrain(t, p, u, options())
			task, in := fixture()
			if _, err := b.Reply(context.Background(), task, in); err == nil {
				t.Fatal("错误回复被接受")
			}
			if int(p.calls.Load()) != tc.want || len(u.snapshot()) != tc.want {
				t.Fatalf("重试/记账次数错误：%d/%d", p.calls.Load(), len(u.snapshot()))
			}
			for _, row := range u.snapshot() {
				if row.OK || row.CostUSD != nil || row.InputTokens != nil {
					t.Fatal("未知用量被改成已知0")
				}
			}
		})
	}
	p := &fakeProvider{call: func(_ context.Context, _ provider.Request, n int) (provider.Response, error) {
		if n == 1 {
			return provider.Response{}, &provider.Error{Kind: provider.KindHTTP, Retryable: true}
		}
		return provider.Response{Text: "成功"}, nil
	}}
	u := &fakeUsage{}
	b := makeBrain(t, p, u, options())
	task, in := fixture()
	if _, err := b.Reply(context.Background(), task, in); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 || !u.snapshot()[1].OK {
		t.Fatal("重试成功未记账")
	}
}

func TestBudgetMemoryBeforePersonaAndNoMutation(t *testing.T) {
	task, in := fixture()
	in.Memory.Records = []memory.Record{{ID: "low", Summary: "LOW" + strings.Repeat("a", 400), Importance: .1, Confidence: .8}, {ID: "high", Summary: "HIGH" + strings.Repeat("b", 400), Importance: .9, Confidence: .8}}
	p, u := &fakeProvider{}, &fakeUsage{}
	o := options()
	o.MaxPromptTokens = 1500
	b := makeBrain(t, p, u, o)
	if _, err := b.Reply(context.Background(), task, in); err != nil {
		t.Fatal(err)
	}
	r := p.last()
	if strings.Contains(r.Messages[1].Content, "LOW") || !strings.Contains(r.Messages[1].Content, "HIGH") {
		t.Fatal("未优先裁低重要性记忆")
	}
	if r.Messages[0].Content != in.Persona.SystemPrompt() || len(in.Memory.Records) != 2 || in.Memory.Records[0].ID != "low" {
		t.Fatal("修改了人格或原始记忆")
	}
	if len(r.Messages[0].Content)+len(r.Messages[1].Content)+64 > o.MaxPromptTokens {
		t.Fatal("预算超限仍请求")
	}
}

func TestBudgetPersonaSuffixAndImpossibleSoul(t *testing.T) {
	task, in := fixture()
	in.Persona.Soul = "必须保留的身份"
	in.Persona.Rules = strings.Repeat("规", 1000)
	in.Persona.Samples = strings.Repeat("样", 1000)
	p, u := &fakeProvider{}, &fakeUsage{}
	o := options()
	o.MaxPromptTokens = 1000
	b := makeBrain(t, p, u, o)
	if _, err := b.Reply(context.Background(), task, in); err != nil {
		t.Fatal(err)
	}
	r := p.last()
	if !strings.HasPrefix(r.Messages[0].Content, in.Persona.Soul) || !utf8.ValidString(r.Messages[0].Content) || len(in.Persona.Rules) != 3000 {
		t.Fatal("soul被删、切坏中文或修改入参")
	}
	p, u = &fakeProvider{}, &fakeUsage{}
	o.MaxPromptTokens = 64
	b = makeBrain(t, p, u, o)
	if _, err := b.Reply(context.Background(), task, in); err == nil {
		t.Fatal("不可能预算未拒绝")
	}
	if p.calls.Load() != 0 || len(u.snapshot()) != 0 {
		t.Fatal("预算拒绝仍产生调用")
	}
}

func TestCancellationStillAccountsAndUsageFailureStops(t *testing.T) {
	task, in := fixture()
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakeProvider{call: func(ctx context.Context, _ provider.Request, _ int) (provider.Response, error) {
		cancel()
		return provider.Response{}, &provider.Error{Kind: provider.KindCanceled, Cause: ctx.Err()}
	}}
	u := &fakeUsage{}
	b := makeBrain(t, p, u, options())
	if _, err := b.Reply(ctx, task, in); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误链丢失：%v", err)
	}
	if len(u.snapshot()) != 1 || !u.contextOK {
		t.Fatal("取消后未使用有界清理Context记账")
	}
	p = &fakeProvider{}
	u = &fakeUsage{err: errors.New("private-database-detail")}
	b = makeBrain(t, p, u, options())
	_, err := b.Reply(context.Background(), task, in)
	if err == nil || strings.Contains(err.Error(), "private-database-detail") || p.calls.Load() != 1 {
		t.Fatal("用量失败被吞掉、泄露或触发重复生成")
	}
}

func TestConcurrencyWaitHonorsCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p := &fakeProvider{call: func(ctx context.Context, _ provider.Request, _ int) (provider.Response, error) {
		close(entered)
		select {
		case <-release:
			return provider.Response{Text: "好"}, nil
		case <-ctx.Done():
			return provider.Response{}, ctx.Err()
		}
	}}
	u := &fakeUsage{}
	o := options()
	o.MaxConcurrent = 1
	b := makeBrain(t, p, u, o)
	task, in := fixture()
	done := make(chan error, 1)
	go func() { _, err := b.Reply(context.Background(), task, in); done <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := b.Reply(ctx, task, in)
	if !errors.Is(err, context.DeadlineExceeded) || p.calls.Load() != 1 {
		t.Fatal("等待并发槽没有遵守取消")
	}
	var timeout *Error
	if !errors.As(err, &timeout) || timeout.Kind != KindTimeout || !timeout.Retryable {
		close(release)
		if cleanupErr := <-done; cleanupErr != nil {
			t.Error(cleanupErr)
		}
		t.Fatal("等待超时应允许上层用新Context重试")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(u.snapshot()) != 1 {
		t.Fatal("未实际调用却产生账本")
	}
}

func TestTotalDeadlineIsRetryableButCancelIsNot(t *testing.T) {
	task, in := fixture()
	p := &fakeProvider{call: func(ctx context.Context, _ provider.Request, _ int) (provider.Response, error) {
		<-ctx.Done()
		return provider.Response{}, &provider.Error{Kind: provider.KindTimeout, Retryable: true, Cause: ctx.Err()}
	}}
	o := options()
	o.TotalTimeout = 5 * time.Millisecond
	b := makeBrain(t, p, &fakeUsage{}, o)
	_, err := b.Reply(context.Background(), task, in)
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != KindTimeout || !failure.Retryable || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("总超时不应当作永久错误：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = b.Reply(ctx, task, in)
	if !errors.As(err, &failure) || failure.Retryable || !errors.Is(err, context.Canceled) {
		t.Fatal("主动取消不应重试")
	}
}

func TestTotalDeadlineIncludesPreparation(t *testing.T) {
	task, in := fixture()
	for i := range 2000 {
		in.Memory.Records = append(in.Memory.Records, memory.Record{ID: fmt.Sprintf("r-%04d", i), Summary: strings.Repeat("m", 200), Importance: .5, Confidence: .8})
	}
	p, u := &fakeProvider{}, &fakeUsage{}
	o := options()
	o.TotalTimeout = time.Nanosecond
	b := makeBrain(t, p, u, o)
	started := time.Now()
	_, err := b.Reply(context.Background(), task, in)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("准备阶段应遵守总超时：%v", err)
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("取消后仍在长时间准备预算")
	}
	if p.calls.Load() != 0 || len(u.snapshot()) != 0 {
		t.Fatal("过期准备仍调用或记账")
	}
}

func TestOversizeInputRejectedBeforeProvider(t *testing.T) {
	task, in := fixture()
	in.Memory.Records = []memory.Record{{ID: "large", Summary: strings.Repeat("x", (4<<20)+1), Importance: .5, Confidence: .8}}
	p, u := &fakeProvider{}, &fakeUsage{}
	b := makeBrain(t, p, u, options())
	_, err := b.Reply(context.Background(), task, in)
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != KindInput {
		t.Fatal("超大输入应在准备前拒绝")
	}
	if p.calls.Load() != 0 {
		t.Fatal("超大输入仍触发Provider")
	}
}
