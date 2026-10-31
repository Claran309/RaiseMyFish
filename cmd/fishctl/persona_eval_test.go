package main

import (
	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/personaeval"
	"RaiseMyFish/internal/store"
	"bytes"
	"context"
	"go.uber.org/zap"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type evalUsageStub struct{}

func (evalUsageStub) RecordUsage(context.Context, store.Usage) error { return nil }

func TestEvalGeneratorHasRealTraceAndDoesNotUseLabels(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"unit-served-model","choices":[{"message":{"content":"鱼鱼鱼"}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`)
	}))
	defer s.Close()
	p, err := provider.NewOpenAI("unit", s.URL, "unit-secret", s.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	audit := &evalProvider{inner: p}
	usage := &evalUsage{inner: evalUsageStub{}}
	o := brain.DefaultOptions()
	o.Model = "unit"
	b, err := brain.New(audit, usage, o, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	pack, err := persona.Load(context.Background(), "../../personas/dafeiyu")
	if err != nil {
		t.Fatal(err)
	}
	g := &evalGenerator{runtime: b, pack: pack, provider: audit, usage: usage, runID: "unit"}
	got, err := g.Generate(context.Background(), personaeval.Input{ID: "dm-1", Channel: event.ChDM, Text: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	if got.RawOutput != "鱼鱼鱼" || got.DeliveredOutput != "鱼鱼鱼" || len(got.Attempts) != 1 || !got.Attempts[0].LedgerStored || got.Attempts[0].ServedModel != "unit-served-model" || got.Attempts[0].SystemSHA256 == "" {
		t.Fatal("真实调用审计字段缺失")
	}
}

func TestEvalCLIRejectsBaselineAndRequiresEnabledConfig(t *testing.T) {
	if err := runPersonaEval(context.Background(), "", "", []string{"--baseline", "file"}, io.Discard, io.Discard); err == nil {
		t.Fatal("baseline入口未拒绝")
	}
	t.Setenv("FISH_DB_DSN", "postgres://unused")
	cfg := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(cfg, []byte("[llm]\nenabled=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runPersonaEval(context.Background(), cfg, "", nil, &out, io.Discard); err == nil {
		t.Fatal("未启用模型就运行真实评估")
	}
}
