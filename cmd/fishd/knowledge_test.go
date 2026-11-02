package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"go.uber.org/zap"
)

func TestKnowledgeAssemblyAndDiagnosticWithoutBrainCalls(t *testing.T) {
	cfg := config.Knowledge{Enabled: true, Dir: "../../personas/dafeiyu/knowledge", BudgetPercent: 10}
	a, err := buildKnowledge(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a.Reader == nil || !a.Diagnostic.Assembled || a.Diagnostic.Entries < 1 || len(a.Diagnostic.Revision) != 64 || a.Diagnostic.PipelineConnected {
		t.Fatal("未正确装配不可变Reader快照")
	}
	var out bytes.Buffer
	if err := printCheckDiagnostic(config.Config{Knowledge: cfg}, nil, a, &out); err != nil {
		t.Fatal(err)
	}
	var result struct {
		ProviderCalled bool                `json:"provider_called"`
		Knowledge      knowledgeDiagnostic `json:"knowledge"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.ProviderCalled || result.Knowledge.PipelineConnected || result.Knowledge.Entries < 1 {
		t.Fatal("check输出错误")
	}
	if bytes.Contains(out.Bytes(), []byte("personas/")) || bytes.Contains(out.Bytes(), []byte("主题：")) {
		t.Fatal("诊断暴露路径或正文")
	}
	// 已装配快照元数据不触碰源目录：删掉临时源后依旧可查询metadata。
	dir := t.TempDir()
	outputRoot, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := outputRoot.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"lore.md", "memes.md", "viewers.md", "games.md"} {
		data, err := os.ReadFile(filepath.Join(cfg.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := outputRoot.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Dir = dir
	b, err := buildKnowledge(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "memes.md")); err != nil {
		t.Fatal(err)
	}
	info, err := b.Reader.(*knowledge.FileReader).SnapshotInfo()
	if err != nil || info.Entries != 500 || info.Revision != b.Diagnostic.Revision {
		t.Fatal("metadata重新读文件或不来自同一快照")
	}
}

func TestKnowledgeAssemblyOffErrorsAndCancellation(t *testing.T) {
	cfg := config.Knowledge{Dir: filepath.Join(t.TempDir(), "missing"), BudgetPercent: 10}
	a, err := buildKnowledge(context.Background(), cfg)
	if err != nil || a.Reader != nil || a.Diagnostic.Assembled {
		t.Fatal("关闭路径读目录或伪装装配")
	}
	cfg.Enabled = true
	if _, err := buildKnowledge(context.Background(), cfg); err == nil || bytes.Contains([]byte(err.Error()), []byte(cfg.Dir)) {
		t.Fatal("缺目录隐藏成空命中或泄露路径")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg.Dir = "../../personas/dafeiyu/knowledge"
	if _, err := buildKnowledge(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatal("取消链丢失")
	}
}

func TestKnowledgeEmptyLibraryIsAssembledAndNotLoadFailure(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lore.md", "memes.md", "viewers.md", "games.md"} {
		if err := root.WriteFile(name, []byte("---\nschema: fish-knowledge/v1\nentries: []\n---\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := buildKnowledge(context.Background(), config.Knowledge{Enabled: true, Dir: dir, BudgetPercent: 0})
	if err != nil || a.Reader == nil || !a.Diagnostic.Assembled || a.Diagnostic.Entries != 0 || len(a.Diagnostic.Revision) != 64 {
		t.Fatal("合法空库和加载失败混淆")
	}
}

func TestBrainAssemblyUsesConfiguredKnowledgePercent(t *testing.T) {
	// 仅内存测试显式调用，证明配置到Brain的参数接缝；不表示服务worker已接通。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"本鱼在"}}]}`)
	}))
	defer server.Close()
	cfg := config.Config{Knowledge: config.Knowledge{Enabled: true, Dir: "../../personas/dafeiyu/knowledge", BudgetPercent: 0}, LLM: config.LLM{Enabled: true, PersonaDir: "../../personas/dafeiyu", MaxPromptTokens: 32768, MaxReplyTokens: 128, AttemptTimeoutMS: 1000, TotalTimeoutMS: 5000, BackoffMS: 1, MaxConcurrent: 1, Temperature: .7, Providers: map[string]config.Provider{"fake": {Type: "openai", BaseURL: server.URL, APIKey: "unit-only", Models: []string{"model"}}}, Routing: map[string]string{"default": "fake:model"}}}
	routes, pack, err := buildBrains(context.Background(), cfg, &diagnosticUsage{}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	scope := memory.Scope{Channel: event.ChLive, Session: "unit"}
	task := brain.Task{ID: "cfg-test", EventID: "live:unit:1", Channel: scope.Channel, Session: scope.Session}
	in := brain.ContextInput{Event: event.Event{ID: task.EventID, Channel: task.Channel, Session: task.Session, Dir: event.DirIn, Text: "单位测试", At: time.Now()}, Persona: pack, Memory: memory.Context{Scope: scope}, Shared: memory.SharedState{Scope: scope}, Knowledge: &knowledge.Context{Scope: knowledge.Scope{Channel: scope.Channel, Session: scope.Session}, Revision: strings.Repeat("a", 64), Items: []knowledge.Item{{ID: "k", Source: "test:config", Text: "知识资料", Scenes: []string{"live"}, Priority: 50}}}}
	reply, err := routes["default"].Reply(context.Background(), task, in)
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Knowledge struct {
			BudgetPercent int      `json:"budget_percent"`
			IncludedIDs   []string `json:"included_ids"`
			DroppedIDs    []string `json:"dropped_ids"`
		} `json:"knowledge"`
	}
	if err := json.Unmarshal(reply.Meta, &meta); err != nil || meta.Knowledge.BudgetPercent != 0 || len(meta.Knowledge.IncludedIDs) != 0 || len(meta.Knowledge.DroppedIDs) != 1 {
		t.Fatal("配置0%未传入Brain")
	}
}
