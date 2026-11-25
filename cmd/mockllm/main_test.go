package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestServer(t *testing.T, cfg Config) *server {
	t.Helper()
	srv, err := newServer(cfg)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return srv
}

func postChat(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestChatNormalReply(t *testing.T) {
	srv := newTestServer(t, Config{Latency: 0, PromptTokens: 800, CompletionTokens: 20, Reply: "压测回复"})
	rec := postChat(t, srv.handler(), `{"model":"mock","messages":[{"role":"user","content":"你好"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Model != "mock" || len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "压测回复" {
		t.Fatalf("unexpected resp: %+v", resp)
	}
	if resp.Usage.PromptTokens != 800 || resp.Usage.CompletionTokens != 20 || resp.Usage.TotalTokens != 820 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestChatReActProtocol(t *testing.T) {
	srv := newTestServer(t, Config{Reply: "收到"})
	body := `{"model":"mock","messages":[{"role":"system","content":"每一步只输出一个 JSON 对象"},{"role":"user","content":"hi"}]}`
	rec := postChat(t, srv.handler(), body)
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	content := resp.Choices[0].Message.Content
	var step map[string]any
	if err := json.Unmarshal([]byte(content), &step); err != nil {
		t.Fatalf("ReAct 回复不是合法 JSON: %q", content)
	}
	if step["action"] != "final" || step["final"] != "收到" {
		t.Fatalf("unexpected step: %v", step)
	}
}

func TestChatInjectedError(t *testing.T) {
	srv := newTestServer(t, Config{ErrorRate: 1})
	rec := postChat(t, srv.handler(), `{"model":"mock","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", rec.Code)
	}
	if srv.errors.Load() != 1 {
		t.Fatalf("errors=%d, want 1", srv.errors.Load())
	}
}

func TestChatInjectedTimeout(t *testing.T) {
	srv := newTestServer(t, Config{TimeoutRate: 1, TimeoutDelay: 80 * time.Millisecond})
	start := time.Now()
	rec := postChat(t, srv.handler(), `{"model":"mock","messages":[{"role":"user","content":"hi"}]}`)
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("未等待注入的超时延迟: %s", elapsed)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("超时请求不应返回正文: %q", rec.Body.String())
	}
	if srv.timeouts.Load() != 1 {
		t.Fatalf("timeouts=%d, want 1", srv.timeouts.Load())
	}
}

func TestChatRejectsBadRequest(t *testing.T) {
	srv := newTestServer(t, Config{})
	for _, body := range []string{`not-json`, `{"model":"","messages":[]}`} {
		rec := postChat(t, srv.handler(), body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%q status=%d, want 400", body, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d, want 405", rec.Code)
	}
}

func TestStatsEndpoint(t *testing.T) {
	srv := newTestServer(t, Config{})
	for i := 0; i < 3; i++ {
		postChat(t, srv.handler(), `{"model":"mock","messages":[{"role":"user","content":"hi"}]}`)
	}
	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)
	var stats map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats["requests"] != 3 {
		t.Fatalf("requests=%d, want 3", stats["requests"])
	}
}

func TestChatConcurrentSafe(t *testing.T) {
	srv := newTestServer(t, Config{ErrorRate: 0.5, Seed: 7})
	h := srv.handler()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"mock","messages":[{"role":"user","content":"hi"}]}`))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			_, _ = io.Copy(io.Discard, rec.Body)
		}()
	}
	wg.Wait()
	if srv.requests.Load() != 32 {
		t.Fatalf("requests=%d, want 32", srv.requests.Load())
	}
}

func TestValidateAddr(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:18080", true},
		{"localhost:18080", true},
		{"[::1]:18080", true},
		{"0.0.0.0:18080", false},
		{"192.168.1.2:18080", false},
		{"127.0.0.1", false},
		{"127.0.0.1:abc", false},
	}
	for _, c := range cases {
		err := validateAddr(c.addr)
		if (err == nil) != c.ok {
			t.Fatalf("addr=%s err=%v, want ok=%v", c.addr, err, c.ok)
		}
	}
}

func TestNewServerRejectsBadConfig(t *testing.T) {
	bad := []Config{
		{Latency: -time.Second},
		{Latency: 11 * time.Second},
		{PromptTokens: -1},
		{ErrorRate: 1.5},
		{TimeoutRate: -0.1},
	}
	for i, cfg := range bad {
		if _, err := newServer(cfg); err == nil {
			t.Fatalf("case %d 应该报错: %+v", i, cfg)
		}
	}
}
