package watch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckASRHealthy(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer server.Close()

	if err := CheckASR(context.Background(), server.URL+"/v1/audio/transcriptions", server.Client(), time.Second); err != nil {
		t.Fatalf("健康服务被误报：%v", err)
	}
	if gotPath != "/health" {
		t.Fatalf("探测路径=%q，期望 /health", gotPath)
	}
}

func TestCheckASRFailuresAreExplicit(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"服务端错误", http.StatusInternalServerError, "HTTP 500"},
		{"端点不存在", http.StatusNotFound, "HTTP 404"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, "secret-body")
			}))
			defer server.Close()
			err := CheckASR(context.Background(), server.URL+"/v1/audio/transcriptions", server.Client(), time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误=%v，期望包含 %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-body") {
				t.Fatal("错误泄露了响应正文")
			}
		})
	}

	// 端口不可达：必须显式报错，且带上主机便于定位。
	err := CheckASR(context.Background(), "http://127.0.0.1:1/v1/audio/transcriptions", nil, 500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "不可达") || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("不可达服务未显式报错：%v", err)
	}

	if err := CheckASR(context.Background(), "ftp://127.0.0.1/x", nil, time.Second); err == nil {
		t.Fatal("接受非法协议")
	}
}

func TestASRHost(t *testing.T) {
	if got := ASRHost("http://127.0.0.1:8077/v1/audio/transcriptions"); got != "127.0.0.1:8077" {
		t.Fatalf("host=%q", got)
	}
	if got := ASRHost("not a url"); got != "" {
		t.Fatalf("非法地址应返回空串，实际 %q", got)
	}
}
