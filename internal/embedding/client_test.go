package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type embedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions"`
}

func mockEmbeddingServer(t *testing.T, dimension int, handler func(req embedRequest, w http.ResponseWriter)) (*httptest.Server, *[]embedRequest) {
	t.Helper()
	var (
		mu       sync.Mutex
		requests []embedRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" {
			t.Errorf("请求方法或路径错误：%s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("解析请求失败：%v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		handler(req, w)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func newTestClient(t *testing.T, url string, opts Options) *Client {
	t.Helper()
	opts.URL = url + "/v1/embeddings"
	if opts.Model == "" {
		opts.Model = "embedding-3"
	}
	if opts.Dimension == 0 {
		opts.Dimension = 3
	}
	if opts.APIKey == "" {
		opts.APIKey = "test-key"
	}
	client, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestEmbedRequestAndParse 断言 URL/Bearer/模型/维度/批量与响应解析顺序。
func TestEmbedRequestAndParse(t *testing.T) {
	server, requests := mockEmbeddingServer(t, 3, func(req embedRequest, w http.ResponseWriter) {
		data := make([]map[string]any, 0, len(req.Input))
		for i := range req.Input {
			data = append(data, map[string]any{"embedding": []float64{float64(i), 0.5, -1}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	client := newTestClient(t, server.URL, Options{BatchSize: 2})
	vectors, err := client.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 3 || vectors[0][0] != 0 || vectors[1][0] != 1 || vectors[2][0] != 0 {
		t.Fatalf("批量结果顺序错误：%v", vectors)
	}
	if len(*requests) != 2 {
		t.Fatalf("应按 batch_size=2 拆成两次请求，实际 %d", len(*requests))
	}
	for _, req := range *requests {
		if req.Model != "embedding-3" || req.Dimensions != 3 {
			t.Fatalf("请求字段错误：%+v", req)
		}
	}
	if len((*requests)[0].Input) != 2 || len((*requests)[1].Input) != 1 {
		t.Fatalf("批次大小错误：%+v", *requests)
	}
}

// TestEmbedSendsBearerAndContentType 认证头与内容类型正确。
func TestEmbedSendsBearerAndContentType(t *testing.T) {
	var gotAuth, gotType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,2,3]}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Options{APIKey: "secret-key"})
	if _, err := client.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret-key" || gotType != "application/json" {
		t.Fatalf("请求头错误：auth=%q type=%q", gotAuth, gotType)
	}
}

// TestEmbedEmptyInput 空输入不发请求。
func TestEmbedEmptyInput(t *testing.T) {
	server, requests := mockEmbeddingServer(t, 3, func(_ embedRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := newTestClient(t, server.URL, Options{})
	vectors, err := client.Embed(context.Background(), nil)
	if err != nil || len(vectors) != 0 || len(*requests) != 0 {
		t.Fatalf("空输入行为错误：%v %v %d", err, vectors, len(*requests))
	}
}

// TestEmbedErrorKinds 网络/认证/限流/协议分类稳定，且错误不包含密钥。
func TestEmbedErrorKinds(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		kind   Kind
	}{
		{"认证", http.StatusUnauthorized, `{}`, KindAuth},
		{"禁止", http.StatusForbidden, `{}`, KindAuth},
		{"限流", http.StatusTooManyRequests, `{}`, KindRateLimit},
		{"服务端", http.StatusBadGateway, `{}`, KindNetwork},
		{"请求错误", http.StatusBadRequest, `{}`, KindProtocol},
		{"坏JSON", http.StatusOK, `{`, KindProtocol},
		{"条数不符", http.StatusOK, `{"data":[]}`, KindProtocol},
		{"维度不符", http.StatusOK, `{"data":[{"embedding":[1,2]}]}`, KindProtocol},
		{"非法数值", http.StatusOK, `{"data":[{"embedding":[1e999,2,3]}]}`, KindProtocol},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Options{APIKey: "must-not-leak"})
			_, err := client.Embed(context.Background(), []string{"x"})
			if err == nil || KindOf(err) != tc.kind {
				t.Fatalf("分类错误：%v kind=%s want=%s", err, KindOf(err), tc.kind)
			}
			if strings.Contains(err.Error(), "must-not-leak") {
				t.Fatal("错误信息泄露密钥")
			}
		})
	}
}

// TestEmbedNetworkError 服务不可达归为网络错误。
func TestEmbedNetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()
	client := newTestClient(t, url, Options{})
	_, err := client.Embed(context.Background(), []string{"x"})
	if err == nil || KindOf(err) != KindNetwork {
		t.Fatalf("网络错误分类不符：%v", err)
	}
}

// TestEmbedTimeout 客户端超时归为网络错误。
func TestEmbedTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,2,3]}]}`))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Options{Timeout: 20 * time.Millisecond})
	_, err := client.Embed(context.Background(), []string{"x"})
	if err == nil || KindOf(err) != KindNetwork {
		t.Fatalf("超时分类不符：%v", err)
	}
}

// TestEmbedContextCanceled 取消上下文时返回取消原因而不是伪装成网络错误。
func TestEmbedContextCanceled(t *testing.T) {
	server, _ := mockEmbeddingServer(t, 3, func(_ embedRequest, w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,2,3]}]}`))
	})
	client := newTestClient(t, server.URL, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Embed(ctx, []string{"x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误未保留：%v", err)
	}
}

// TestNewRejectsUnsafeOptions 拒绝非本机 HTTP、空模型与非法维度/批量。
func TestNewRejectsUnsafeOptions(t *testing.T) {
	for _, opts := range []Options{
		{URL: "http://example.com/embed", Model: "m"},
		{URL: "https://example.com/embed?token=x", Model: "m"},
		{URL: "https://example.com/embed", Model: ""},
		{URL: "https://example.com/embed", Model: "m", Dimension: 2049},
		{URL: "https://example.com/embed", Model: "m", BatchSize: -1},
	} {
		if _, err := New(opts); err == nil {
			t.Fatalf("非法参数未被拒绝：%+v", opts)
		}
	}
}
