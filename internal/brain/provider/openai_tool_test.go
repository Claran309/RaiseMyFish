package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func toolRequest() ToolRequest {
	return ToolRequest{
		Model:       "fake-model",
		Messages:    []Message{{Role: "system", Content: "人格"}, {Role: "user", Content: "问题"}},
		MaxTokens:   64,
		Temperature: .7,
		Tools: []ToolDefinition{{
			Type: "function",
			Function: ToolFunction{
				Name:        "memory_search",
				Description: "查记忆",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
			},
		}},
	}
}

func TestOpenAIChatWithToolsParsesCallsAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer fake-key" {
			t.Error("请求路径或认证错误")
		}
		var body struct {
			Model     string           `json:"model"`
			Messages  []Message        `json:"messages"`
			MaxTokens int              `json:"max_tokens"`
			Tools     []ToolDefinition `json:"tools"`
			Stream    bool             `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "fake-model" || body.MaxTokens != 64 || body.Stream || len(body.Messages) != 2 || len(body.Tools) != 1 || body.Tools[0].Function.Name != "memory_search" {
			t.Error("请求字段不正确")
		}
		_, _ = io.WriteString(w, `{"model":"served-v1","choices":[{"message":{"content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"memory_search","arguments":"{\"query\":\"白饭\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":3}}}`)
	}))
	defer server.Close()
	p, err := NewOpenAI("fake", server.URL+"/v1", "fake-key", server.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ChatWithTools(context.Background(), toolRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "" || len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call_1" || got.ToolCalls[0].Function.Name != "memory_search" || got.ToolCalls[0].Function.Arguments != `{"query":"白饭"}` {
		t.Fatalf("工具调用解析错误：%+v", got)
	}
	if got.Model != "served-v1" || got.Usage.InputTokens == nil || *got.Usage.InputTokens != 10 || got.Usage.OutputTokens == nil || *got.Usage.OutputTokens != 5 || got.Usage.CachedInputTokens == nil || *got.Usage.CachedInputTokens != 3 {
		t.Fatalf("用量解析错误：%+v", got)
	}
}

func TestOpenAIChatWithToolsRoundTripsToolMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Messages) != 4 || len(body.Messages[2].ToolCalls) != 1 || body.Messages[3].Role != "tool" || body.Messages[3].ToolCallID != "call_1" {
			t.Error("tool 消息没有按协议回填")
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"查到了"}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`)
	}))
	defer server.Close()
	p, err := NewOpenAI("fake", server.URL+"/v1", "fake-key", server.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	in := toolRequest()
	in.Messages = []Message{
		{Role: "system", Content: "人格"},
		{Role: "user", Content: "问题"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "memory_search", Arguments: `{"query":"白饭"}`}}}},
		{Role: "tool", Content: "结果", ToolCallID: "call_1"},
	}
	got, err := p.ChatWithTools(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "查到了" || len(got.ToolCalls) != 0 {
		t.Fatalf("最终正文解析错误：%+v", got)
	}
}

func TestOpenAIChatWithToolsUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"tools not supported"}`)
	}))
	defer server.Close()
	p, err := NewOpenAI("fake", server.URL+"/v1", "fake-key", server.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.ChatWithTools(context.Background(), toolRequest())
	var callErr *Error
	if !errors.As(err, &callErr) || callErr.Kind != KindUnsupported || callErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("不支持 tools 的错误分类错误：%v", err)
	}
	if strings.Contains(err.Error(), "tools not supported") {
		t.Fatal("错误泄露响应正文")
	}
}

func TestOpenAIChatWithToolsRejectsInvalidRequest(t *testing.T) {
	p, err := NewOpenAI("fake", "http://127.0.0.1:1/v1", "fake-key", &http.Client{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cases := []ToolRequest{
		{Model: "m", Messages: []Message{{Role: "tool", Content: "x"}}, MaxTokens: 1, Temperature: .7},
		{Model: "m", Messages: []Message{{Role: "system", Content: "s"}}, MaxTokens: 1, Temperature: .7, Tools: []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "", Parameters: json.RawMessage(`{}`)}}}},
		{Model: "m", Messages: []Message{{Role: "system", Content: "s"}}, MaxTokens: 1, Temperature: .7, Tools: []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "x", Parameters: json.RawMessage(`not-json`)}}}},
	}
	for i, in := range cases {
		if _, err := p.ChatWithTools(context.Background(), in); err == nil {
			t.Fatalf("用例 %d 应被拒绝", i)
		}
	}
}
