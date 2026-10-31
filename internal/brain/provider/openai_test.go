package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func request() Request {
	return Request{Model: "fake-model", Messages: []Message{{Role: "system", Content: "人格"}, {Role: "user", Content: "问题"}}, MaxTokens: 64, Temperature: .7}
}

func TestOpenAISuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fake-key" {
			t.Error("请求路径或认证错误")
		}
		var body struct {
			Model     string    `json:"model"`
			Messages  []Message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
			Stream    bool      `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "fake-model" || body.MaxTokens != 64 || body.Stream || len(body.Messages) != 2 {
			t.Error("请求字段不正确")
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"你好鱼"}}],"usage":{"prompt_tokens":0,"completion_tokens":3}}`)
	}))
	defer server.Close()
	p, err := NewOpenAI("fake", server.URL+"/v1", "fake-key", server.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Chat(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "fake" || got.Text != "你好鱼" || got.Usage.InputTokens == nil || *got.Usage.InputTokens != 0 || *got.Usage.OutputTokens != 3 {
		t.Fatal("响应或已知0计数错误")
	}
}

func TestOpenAIFailuresAreSafeAndClassified(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		body  string
		retry bool
		kind  Kind
	}{
		{"4xx", 401, `secret-response`, false, KindHTTP},
		{"429", 429, `secret-response`, false, KindHTTP},
		{"5xx", 503, `secret-response`, true, KindHTTP},
		{"json", 200, `secret-response`, false, KindProtocol},
		{"negative_tokens", 200, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":-1}}`, false, KindProtocol},
		{"tool_calls", 200, `{"choices":[{"message":{"tool_calls":[{"id":"x"}]}}]}`, false, KindProtocol},
		{"oversized", 200, strings.Repeat("x", (2<<20)+1), false, KindProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer s.Close()
			p, err := NewOpenAI("fake", s.URL, "secret-key", s.Client(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Chat(context.Background(), request())
			var callErr *Error
			if !errors.As(err, &callErr) || callErr.Kind != tc.kind || callErr.Retryable != tc.retry {
				t.Fatalf("分类错误：%v", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("错误泄露请求或响应内容")
			}
			if calls.Load() != 1 {
				t.Fatal("Provider不应自行重试")
			}
		})
	}
}

func TestOpenAIUnknownUsageAndEmpty(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":""}}]}`)
	}))
	defer s.Close()
	p, err := NewOpenAI("fake", s.URL, "", s.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response, err := p.Chat(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "" || response.Usage.InputTokens != nil || response.Usage.OutputTokens != nil {
		t.Fatal("未知用量被改为0")
	}
}

func TestOpenAITimeoutCancellationAndRedirect(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer slow.Close()
	p, err := NewOpenAI("fake", slow.URL, "", slow.Client(), 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Chat(context.Background(), request())
	var callErr *Error
	if !errors.As(err, &callErr) || callErr.Kind != KindTimeout || !callErr.Retryable {
		t.Fatalf("超时分类错误：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Chat(ctx, request())
	if !errors.Is(err, context.Canceled) {
		t.Fatal("取消错误链丢失")
	}
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	p, err = NewOpenAI("fake", redirect.URL, "secret-key", redirect.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Chat(context.Background(), request()); err == nil {
		t.Fatal("接受重定向")
	}
	if reached.Load() != 0 {
		t.Fatal("向重定向目标泄露了认证")
	}
}

func TestOpenAIRejectsUnsafeConfigAndRequest(t *testing.T) {
	for _, base := range []string{"http://example.com/v1", "https://user:secret@example.com/v1", "https://example.com/v1?key=secret", "https://example.com/#secret", "not-url"} {
		if _, err := NewOpenAI("fake", base, "k", &http.Client{}, time.Second); err == nil {
			t.Fatal("接受无效或不安全端点")
		}
	}
	p, err := NewOpenAI("fake", "http://127.0.0.1:1", "", &http.Client{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r := request()
	r.Model = ""
	if _, err := p.Chat(context.Background(), r); err == nil {
		t.Fatal("接受无效请求")
	}
}

func TestProtocolErrorKeepsKnownUsage(t *testing.T) {
	for _, choices := range []string{`[]`, `[{"message":{"tool_calls":[{"id":"unused"}]}}]`, `[{"message":{"content":{}}}]`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"choices":`+choices+`,"usage":{"prompt_tokens":7,"completion_tokens":2}}`)
		}))
		p, err := NewOpenAI("fake", s.URL, "", s.Client(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		response, err := p.Chat(context.Background(), request())
		s.Close()
		if err == nil {
			t.Fatal("协议错误未被拒绝")
		}
		if response.Usage.InputTokens == nil || response.Usage.OutputTokens == nil || *response.Usage.InputTokens != 7 || *response.Usage.OutputTokens != 2 {
			t.Fatal("已知计费数据在正文协议错误时丢失")
		}
	}
}
