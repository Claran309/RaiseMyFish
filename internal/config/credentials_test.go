package config

// 本文件测「结构校验」与「B站凭据校验」的拆分：
// DB 活跃账号可能晚于加载才回填，因此 LoadForDaemon 放行、ValidateCredentials 拒绝。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadDaemonConfig 写配置并走 fishd 启动用的结构加载；环境中的B站凭据由调用方控制。
func loadDaemonConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	return LoadForDaemon(path, "")
}

func clearBiliCredentialEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"FISH_BILI_COOKIE", "FISH_BILI_COOKIE_FILE", "FISH_BILI_SELF_ID"} {
		t.Setenv(key, "")
	}
}

// TestDaemonLoadDefersLiveCredential DB 账号回填前只做结构校验。
func TestDaemonLoadDefersLiveCredential(t *testing.T) {
	clearBiliCredentialEnv(t)
	body := "[channels.live]\nenabled=true\n[collector.live]\nroom_id='1'\n"
	cfg, err := loadDaemonConfig(t, body)
	if err != nil {
		t.Fatalf("结构加载不应要求B站凭据：%v", err)
	}
	if err := cfg.ValidateCredentials(); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("缺凭据时应被凭据校验拒绝，实际 %v", err)
	}
	// 模拟 DB 活跃账号回填：cookie 与 self_id 同时补上。
	cfg.Collector.Live.Cookie = "SESSDATA=db-canary"
	cfg.Collector.Live.SelfID = "42"
	if err := cfg.ValidateCredentials(); err != nil {
		t.Fatalf("回填 DB 凭据后应通过：%v", err)
	}
}

// TestLoadStillRejectsMissingLiveCredential 非 fishd 入口仍保持一次性完整校验。
func TestLoadStillRejectsMissingLiveCredential(t *testing.T) {
	clearBiliCredentialEnv(t)
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte("[channels.live]\nenabled=true\n[collector.live]\nroom_id='1'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	if _, err := Load(path, ""); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("Load 应拒绝缺失的B站凭据，实际 %v", err)
	}
}

// TestValidateCredentialsCommentNeedsCookieAndSelfID 评论采集的两项依赖分别校验。
func TestValidateCredentialsCommentNeedsCookieAndSelfID(t *testing.T) {
	clearBiliCredentialEnv(t)
	body := "[channels.comment]\nenabled=true\n[collector.comment]\nenabled=true\nvideos=['1']\n"
	cfg, err := loadDaemonConfig(t, body)
	if err != nil {
		t.Fatalf("结构加载失败：%v", err)
	}
	cfg.Collector.Live.Cookie = "SESSDATA=db-canary"
	cfg.Collector.Live.SelfID = "42"
	if err := cfg.ValidateCredentials(); err != nil {
		t.Fatalf("有 cookie 与 self_id 时应通过：%v", err)
	}
	cfg.Collector.Live.SelfID = ""
	if err := cfg.ValidateCredentials(); err == nil || !strings.Contains(err.Error(), "self_id") {
		t.Fatalf("缺 self_id 应拒绝，实际 %v", err)
	}
	cfg.Collector.Live.SelfID = "42"
	cfg.Collector.Live.Cookie = ""
	if err := cfg.ValidateCredentials(); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("缺 cookie 应拒绝，实际 %v", err)
	}
}

// TestValidateCredentialsGiftRealOnly 只有真实送礼才要求凭据，dry-run 放行。
func TestValidateCredentialsGiftRealOnly(t *testing.T) {
	clearBiliCredentialEnv(t)
	cfg, err := loadDaemonConfig(t, "[gift]\nenabled=true\nreal=true\n")
	if err != nil {
		t.Fatalf("结构加载失败：%v", err)
	}
	if err := cfg.ValidateCredentials(); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("真实送礼缺凭据应拒绝，实际 %v", err)
	}
	cfg.Gift.Cookie = "SESSDATA=db-canary"
	if err := cfg.ValidateCredentials(); err != nil {
		t.Fatalf("回填 DB 凭据后应通过：%v", err)
	}

	dry, err := loadDaemonConfig(t, "[gift]\nenabled=true\nreal=false\n")
	if err != nil {
		t.Fatalf("dry-run 结构加载失败：%v", err)
	}
	if err := dry.ValidateCredentials(); err != nil {
		t.Fatalf("dry-run 不应要求凭据：%v", err)
	}
}

// TestValidateCredentialsWatchUsesDBAccount 看直播凭据同样可由 DB 活跃账号回填。
func TestValidateCredentialsWatchUsesDBAccount(t *testing.T) {
	clearBiliCredentialEnv(t)
	t.Setenv("FISH_LLM_KEY", "test-key")
	cfg, err := loadDaemonConfig(t, watchLLMSection+"[watch]\nenabled=true\n")
	if err != nil {
		t.Fatalf("结构加载失败：%v", err)
	}
	if err := cfg.ValidateCredentials(); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("缺看直播凭据应拒绝，实际 %v", err)
	}
	cfg.Watch.Cookie = "SESSDATA=db-canary"
	if err := cfg.ValidateCredentials(); err != nil {
		t.Fatalf("回填 DB 凭据后应通过：%v", err)
	}
}

// TestLoadForCLIStillSkipsLiveCredential CLI 只读命令不因 live 凭据缺失而不可用。
func TestLoadForCLIStillSkipsLiveCredential(t *testing.T) {
	clearBiliCredentialEnv(t)
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte("[channels.live]\nenabled=true\n[collector.live]\nroom_id='1'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	if _, err := LoadForCLI(path, ""); err != nil {
		t.Fatalf("CLI 不应校验 live 凭据：%v", err)
	}
}
