package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

func TestServeCancellationAndFinalState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	path := filepath.Join(t.TempDir(), "status.json")
	calls := 0
	check := func(context.Context) error {
		calls++
		if calls == 2 {
			cancel()
		}
		return nil
	}
	if err := serve(ctx, path, time.Millisecond, zap.NewNop(), check); err != nil {
		t.Fatal(err)
	}
	state, err := infra.ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "stopped" || state.PID <= 0 {
		t.Fatal("取消后未落停止状态")
	}
}

type diagnosticUsage struct{ calls atomic.Int32 }

func (u *diagnosticUsage) RecordUsage(context.Context, store.Usage) error { u.calls.Add(1); return nil }

func TestBrainAssemblyDiagnosticDoesNotCallModel(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer s.Close()
	cfg := config.Config{LLM: config.LLM{Enabled: true, PersonaDir: "../../personas/dafeiyu", MaxPromptTokens: 8192, MaxReplyTokens: 128, AttemptTimeoutMS: 1000, TotalTimeoutMS: 5000, BackoffMS: 1, MaxConcurrent: 1, Temperature: .7,
		Providers: map[string]config.Provider{"fake": {Type: "openai", BaseURL: s.URL, APIKey: "diagnostic-secret", Models: []string{"model"}}}, Routing: map[string]string{"default": "fake:model", "live": "fake:model"}}}
	u := &diagnosticUsage{}
	routes, pack, err := buildBrains(context.Background(), cfg, u, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 || routes["default"] != routes["live"] {
		t.Fatal("重复目标未共享Brain实例")
	}
	var out bytes.Buffer
	if err := printBrainDiagnostic("default", routes, pack, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"provider_called":false`)) || bytes.Contains(out.Bytes(), []byte("diagnostic-secret")) {
		t.Fatal("诊断输出不正确或泄密")
	}
	if calls.Load() != 0 || u.calls.Load() != 0 {
		t.Fatal("诊断不应调用模型或写用量")
	}
	if err := printBrainDiagnostic("missing", routes, pack, io.Discard); err == nil {
		t.Fatal("未知诊断路由未报错")
	}
}

func TestServeDatabaseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	calls := 0
	check := func(context.Context) error {
		calls++
		if calls > 1 {
			return errors.New("数据库断开")
		}
		return nil
	}
	if err := serve(context.Background(), path, time.Millisecond, zap.NewNop(), check); err == nil {
		t.Fatal("数据库故障未退出")
	}
	state, err := infra.ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "stopped" {
		t.Fatal("故障后仍显示运行")
	}
}

func TestRunRejectsUnknownCommandAndMissingConfig(t *testing.T) {
	if err := run(context.Background(), []string{"unknown"}, io.Discard, io.Discard); err == nil {
		t.Fatal("接受未知命令")
	}
	if err := run(context.Background(), []string{"--config", filepath.Join(t.TempDir(), "missing")}, io.Discard, io.Discard); err == nil {
		t.Fatal("接受缺失配置")
	}
}
