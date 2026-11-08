package main

// 本文件回归 O15：buildReasonerFactory 每个事件现造工具箱，
// 但副作用限频表必须跨事件共享，第二个事件不能绕过窗口重复执行。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/reasoning"
	"go.uber.org/zap"
)

type factorySocial struct {
	follows int32
}

func (f *factorySocial) Follow(context.Context, string) error {
	atomic.AddInt32(&f.follows, 1)
	return nil
}
func (f *factorySocial) Unfollow(context.Context, string) error  { return nil }
func (f *factorySocial) Like(context.Context, string, int) error { return nil }
func (f *factorySocial) SendDM(context.Context, string, string) error {
	return nil
}
func (f *factorySocial) PostDynamic(context.Context, string) error { return nil }

// TestReasonerFactorySharesSideEffectLimiterAcrossEvents 两个事件经同一工厂创建 agent，
// 第二个事件的 follow 被第一事件占用的窗口拒绝（执行器只跑一次）。
func TestReasonerFactorySharesSideEffectLimiterAcrossEvents(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		step := `{"thought":"先关注","action":"follow","args":"{\"mid\":\"123\",\"action\":\"follow\"}"}`
		final := `{"thought":"完成","action":"final","final":"好"}`
		content := final
		if n%2 == 1 {
			content = step
		}
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, content)
	}))
	defer server.Close()

	cfg := config.Config{
		LLM: config.LLM{
			Enabled: true, PersonaDir: "../../personas/dafeiyu", MaxPromptTokens: 32768, MaxReplyTokens: 128,
			AttemptTimeoutMS: 1000, TotalTimeoutMS: 5000, BackoffMS: 1, MaxConcurrent: 1, Temperature: .7,
			Providers: map[string]config.Provider{"fake": {Type: "openai", BaseURL: server.URL, APIKey: "unit-only", Models: []string{"model"}}},
			Routing:   map[string]string{"default": "fake:model"},
		},
		Reasoning: config.Reasoning{Enabled: true, MaxSteps: 3, TimeoutMS: 2000, AllowSideEffectTools: true},
		Sender:    &config.Sender{SelfID: "3747574135655903"},
	}
	social := &factorySocial{}
	factory, err := buildReasonerFactory(
		cfg, nil, nil, nil, nil, nil, nil,
		toolExecutors{Social: social}, nil, nil,
		reasoning.NewSideEffectLimiter(), zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if factory == nil {
		t.Fatal("ReAct 未启用")
	}

	scope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	for i := 1; i <= 2; i++ {
		agent, err := factory(scope, "313591854")
		if err != nil {
			t.Fatalf("第 %d 个事件创建 agent 失败：%v", i, err)
		}
		res, err := agent.Run(context.Background(), reasoning.Input{System: "你是测试鱼", Question: "关注一下"})
		if err != nil {
			t.Fatalf("第 %d 个事件执行失败：%v", i, err)
		}
		if res.Text != "好" {
			t.Fatalf("第 %d 个事件回复异常：%+v", i, res)
		}
		if len(res.Tools) != 1 || res.Tools[0] != reasoning.ToolFollow {
			t.Fatalf("第 %d 个事件应调用 follow：%+v", i, res.Tools)
		}
	}
	if got := atomic.LoadInt32(&social.follows); got != 1 {
		t.Errorf("两个事件共用限频，执行器应只跑一次，实际 %d", got)
	}
}
