//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/knowledge"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func knowledgeCheckTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("FISH_TEST_DSN")
	if dsn == "" {
		env, err := godotenv.Read(filepath.Join("..", "..", ".env"))
		if err != nil {
			t.Fatal("需要真实PG测试配置")
		}
		dsn = env["FISH_DB_DSN"]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := database.Open(ctx, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fish_check_%x", suffix)
	quoted := pgx.Identifier{schema}.Sanitize()
	if err := admin.WithContext(ctx).Exec("CREATE SCHEMA " + quoted).Error; err != nil {
		t.Fatal("创建隔离schema失败")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := admin.WithContext(cleanup).Exec("DROP SCHEMA " + quoted + " CASCADE").Error; err != nil {
			t.Error("清理隔离schema失败")
		}
		if err := database.Close(admin); err != nil {
			t.Error(err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("测试DSN格式错误")
		}
		q := u.Query()
		q.Set("search_path", schema+",public")
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " search_path=" + schema + ",public"
}

func TestKnowledgeCheckWithRealPostgresNoModelOrUsage(t *testing.T) {
	dsn := knowledgeCheckTestDSN(t)
	t.Setenv("FISH_DB_DSN", dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	state := filepath.Join(dir, "status.json")
	t.Setenv("FISH_STATE_FILE", state)
	t.Setenv("FISH_LOG_DIR", filepath.Join(dir, "logs"))
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	t.Setenv("FISH_LLM_BASE_URL", server.URL)
	t.Setenv("FISH_LLM_MODEL", "model")
	t.Setenv("FISH_CHECK_UNIT_KEY", "not-real-key")
	path := filepath.Join(dir, "config.toml")
	knowledgeDir, err := filepath.Abs("../../personas/dafeiyu/knowledge")
	if err != nil {
		t.Fatal(err)
	}
	packDir, err := filepath.Abs("../../personas/dafeiyu")
	if err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("[llm]\nenabled = true\npersona_dir = %q\nmax_prompt_tokens = 32768\n[llm.providers.fake]\ntype='openai'\nbase_url=%q\napi_key_env='FISH_CHECK_UNIT_KEY'\nmodels=['model']\n[llm.routing]\ndefault='fake:model'\n[knowledge]\nenabled = true\ndir=%q\nbudget_percent=10\n", filepath.ToSlash(packDir), server.URL, filepath.ToSlash(knowledgeDir))
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--config", path, "--env-file", ""}
	if err := run(ctx, append(append([]string{}, base...), "migrate", "up"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, append(append([]string{}, base...), "check"), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result struct {
		ProviderCalled bool                `json:"provider_called"`
		UsageWritten   bool                `json:"usage_written"`
		BrainRoutes    int                 `json:"brain_routes"`
		Knowledge      knowledgeDiagnostic `json:"knowledge"`
	}
	// 语料会随梗库扩充，期望条数取真实快照而不是硬编码。
	snapshot, err := knowledge.NewFileReader(ctx, knowledgeDir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := snapshot.SnapshotInfo()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.ProviderCalled || result.UsageWritten || result.BrainRoutes != 1 || result.Knowledge.Entries != info.Entries || info.Entries <= 0 || !result.Knowledge.Assembled || result.Knowledge.PipelineConnected {
		t.Fatal("无调用诊断状态错误")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("check写了服务运行状态")
	}
	db, err := database.Open(ctx, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(db); err != nil {
			t.Error(err)
		}
	}()
	var count int64
	if err := db.Table("llm_usage").Count(&count).Error; err != nil || count != 0 || calls.Load() != 0 {
		t.Fatal("诊断调用了模型或写LLM用量")
	}
	missing := filepath.ToSlash(filepath.Join(dir, "must-not-be-read"))
	disabled := strings.Replace(content, "[knowledge]\nenabled = true", "[knowledge]\nenabled = false", 1)
	disabled = strings.Replace(disabled, filepath.ToSlash(knowledgeDir), missing, 1)
	if err := os.WriteFile(path, []byte(disabled), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(ctx, append(append([]string{}, base...), "check"), &output, io.Discard); err != nil {
		t.Fatal("关闭路径访问不存在目录", err)
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Knowledge.Enabled || result.Knowledge.Assembled {
		t.Fatal("关闭诊断伪装启用")
	}
	broken := strings.Replace(disabled, "[knowledge]\nenabled = false", "[knowledge]\nenabled = true", 1)
	if err := os.WriteFile(path, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, append(append([]string{}, base...), "check"), io.Discard, io.Discard); err == nil || strings.Contains(err.Error(), missing) {
		t.Fatal("加载失败被隐藏或回显路径")
	}
	if err := db.Table("llm_usage").Count(&count).Error; err != nil || count != 0 || calls.Load() != 0 {
		t.Fatal("诊断错误路径调用了模型或记用量")
	}
}
