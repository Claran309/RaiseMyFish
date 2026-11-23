package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/reasoning"
	"go.uber.org/zap"
)

func nativeE2EConfig(url string) config.Config {
	cfg := config.Config{}
	cfg.Reasoning = config.DefaultReasoning()
	cfg.Reasoning.NativeTools = "on"
	cfg.Reasoning.MaxSteps = 3
	cfg.Reasoning.TimeoutMS = 3000
	cfg.LLM = config.LLM{
		Enabled: true,
		Providers: map[string]config.Provider{
			"fake": {Type: "openai", BaseURL: url, APIKey: "unit", Models: []string{"m"}},
		},
		Routing:          map[string]string{"default": "fake:m"},
		MaxReplyTokens:   64,
		AttemptTimeoutMS: 3000,
	}
	return cfg
}

// TestNativeToolsEndToEndWithHTTPProvider 覆盖 L1 之前的 B1-D：
// 真实 HTTP 协议 → tool_calls → 工具执行 → tool 回填 → 最终正文。
func TestNativeToolsEndToEndWithHTTPProvider(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Tools    []map[string]any `json:"tools"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("请求不是JSON：%v", err)
			w.WriteHeader(400)
			return
		}
		calls++
		if calls == 1 {
			if len(req.Tools) == 0 {
				t.Error("原生请求缺少 tools")
			}
			_, _ = io.WriteString(w, `{"model":"served","choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"`+reasoning.ToolNow+`","arguments":"{}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
			return
		}
		seenTool := false
		for _, msg := range req.Messages {
			if msg.Role == "tool" {
				seenTool = true
			}
		}
		if !seenTool {
			t.Error("第二轮缺少 tool 消息回填")
		}
		_, _ = io.WriteString(w, `{"model":"served","choices":[{"message":{"content":"现在下午三点"}}],"usage":{"prompt_tokens":12,"completion_tokens":4}}`)
	}))
	defer server.Close()

	factory, err := buildReasonerFactory(nativeE2EConfig(server.URL), nil, nil, nil, nil, nil, nil, nil, toolExecutors{}, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if factory == nil {
		t.Fatal("原生 ReAct 工厂未装配")
	}
	agent, err := factory(memory.Scope{Channel: event.ChDM, Session: "1:2", Private: true}, "2", false, event.ChDM)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agent.RunNative(context.Background(), reasoning.Input{System: "人设", Question: "现在几点"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "现在下午三点" || got.Steps != 2 || len(got.Tools) != 1 || got.Tools[0] != reasoning.ToolNow || got.Degraded {
		t.Fatalf("原生端到端结果错误：%+v", got)
	}
}

// TestNativeToolsAutoFallsBackOnUnsupported 覆盖 auto：Provider 明确不支持 tools 时
// 回落文本协议，事件不会因协议能力缺失而失败。
func TestNativeToolsAutoFallsBackOnUnsupported(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls++
		if strings.Contains(string(body), `"tools"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"tools unsupported"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"served","choices":[{"message":{"content":"文本兜底回复"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	defer server.Close()

	cfg := nativeE2EConfig(server.URL)
	cfg.Reasoning.NativeTools = "auto"
	factory, err := buildReasonerFactory(cfg, nil, nil, nil, nil, nil, nil, nil, toolExecutors{}, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := factory(memory.Scope{Channel: event.ChDM, Session: "1:2", Private: true}, "2", false, event.ChDM)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agent.RunNative(context.Background(), reasoning.Input{System: "人设", Question: "在吗"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "文本兜底回复" || calls != 2 {
		t.Fatalf("auto 降级错误：%+v calls=%d", got, calls)
	}
}
