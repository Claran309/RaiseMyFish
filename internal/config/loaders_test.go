package config

// 本文件测按命令能力拆分的加载器（N17）：
// watchdog 不需要平台凭据/数据库，status/tail 各自只需要本机运行信息；
// 真实发送仍走 LoadForCLI 的凭据校验。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLoaderConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	return path
}

// credentialHeavyConfig 启用所有需要B站凭据的能力：
// 完整加载会因环境无 cookie 被拒，用于证明按命令裁剪后的加载器不再校验这些凭据。
func credentialHeavyConfig() string {
	return `[llm]
enabled=true

[llm.providers.local]
type='openai'
base_url='http://127.0.0.1:18080/v1'
models=['m']

[llm.routing]
default='local:m'

[channels.live]
enabled=true

[collector.live]
room_id='1'

[channels.comment]
enabled=true

[collector.comment]
enabled=true

[channels.dm]
enabled=true

[collector.dm]
enabled=true

[sender.dm]
enabled=true

[watch]
enabled=true

[gift]
enabled=true
real=true

[watchdog]
enabled=true
restart_command='echo restart'
restart_mode='service-command'
`
}

// prepareCredentialFreeEnv 固定环境：B站凭据为空，LLM 指向配置里的本机假端点。
func prepareCredentialFreeEnv(t *testing.T) {
	t.Helper()
	clearBiliCredentialEnv(t)
	// 显式空环境会覆盖 TOML，因此与配置保持一致而不是清空。
	t.Setenv("FISH_BILI_ROOM_ID", "1")
	t.Setenv("FISH_LLM_KEY", "")
	t.Setenv("FISH_LLM_BASE_URL", "http://127.0.0.1:18080/v1")
	t.Setenv("FISH_LLM_MODEL", "m")
	t.Setenv("FISH_STATE_FILE", filepath.Join(t.TempDir(), "fishd-status.json"))
}

// TestLoadForWatchdogSkipsPlatformCredentialsAndDatabase 看门狗只校验自身配置。
func TestLoadForWatchdogSkipsPlatformCredentialsAndDatabase(t *testing.T) {
	prepareCredentialFreeEnv(t)
	path := writeLoaderConfig(t, credentialHeavyConfig())

	// 先证明同一份配置走完整加载会因缺少B站凭据失败。
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	if _, err := Load(path, ""); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("完整加载应因缺少B站凭据失败，实际 %v", err)
	}

	// 看门狗不连数据库：DSN 为空也应能加载。
	t.Setenv("FISH_DB_DSN", "")
	cfg, err := LoadForWatchdog(path, "")
	if err != nil {
		t.Fatalf("看门狗不应要求数据库或平台凭据：%v", err)
	}
	if !cfg.Watchdog.Enabled || cfg.Watchdog.RestartCommand == "" {
		t.Fatal("看门狗配置未保留")
	}
}

// TestLoadForWatchdogKeepsOwnValidation 只跳过无关能力，[watchdog] 自身仍严格。
func TestLoadForWatchdogKeepsOwnValidation(t *testing.T) {
	prepareCredentialFreeEnv(t)
	t.Setenv("FISH_DB_DSN", "")
	path := writeLoaderConfig(t, "[watchdog]\nenabled=true\n")
	if _, err := LoadForWatchdog(path, ""); err == nil || !strings.Contains(err.Error(), "restart_command") {
		t.Fatalf("看门狗自身配置必须校验，实际 %v", err)
	}
}

// TestLoadForStatusSkipsChannelAndSendCredentials status 只读诊断不校验发送凭据。
func TestLoadForStatusSkipsChannelAndSendCredentials(t *testing.T) {
	prepareCredentialFreeEnv(t)
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	path := writeLoaderConfig(t, credentialHeavyConfig())

	cfg, err := LoadForStatus(path, "")
	if err != nil {
		t.Fatalf("status 不应被通道/发送凭据挡住：%v", err)
	}
	if cfg.Database.DSN == "" {
		t.Fatal("status 应解析出数据库连接串")
	}

	// 同一份配置走 CLI 完整加载仍要求评论/私信凭据，证明差异来自命令能力。
	if _, err := LoadForCLI(path, ""); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("CLI 完整加载仍应拒绝缺失凭据，实际 %v", err)
	}
}

// TestLoadForStatusKeepsDatabaseAndStateValidation status 仍需数据库与状态文件有效。
func TestLoadForStatusKeepsDatabaseAndStateValidation(t *testing.T) {
	prepareCredentialFreeEnv(t)

	t.Setenv("FISH_DB_DSN", "")
	if _, err := LoadForStatus(writeLoaderConfig(t, ""), ""); err == nil || !strings.Contains(err.Error(), "数据库") {
		t.Fatalf("缺 DSN 应拒绝，实际 %v", err)
	}

	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	if _, err := LoadForStatus(writeLoaderConfig(t, "[db]\ntimeout_seconds=0\n"), ""); err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("非法超时应拒绝，实际 %v", err)
	}

	t.Setenv("FISH_STATE_FILE", "")
	if _, err := LoadForStatus(writeLoaderConfig(t, ""), ""); err == nil || !strings.Contains(err.Error(), "状态文件") {
		t.Fatalf("空状态文件应拒绝，实际 %v", err)
	}

	t.Setenv("FISH_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
	if _, err := LoadForStatus(writeLoaderConfig(t, "[runtime]\nstale_seconds=0\n"), ""); err == nil || !strings.Contains(err.Error(), "过期阈值") {
		t.Fatalf("非法过期阈值应拒绝，实际 %v", err)
	}

	if _, err := LoadForStatus(writeLoaderConfig(t, ""), ""); err != nil {
		t.Fatalf("最小 status 配置应通过：%v", err)
	}
}

// TestLoadForTailOnlyNeedsLogDir tail 不需要数据库，也不校验任何凭据。
func TestLoadForTailOnlyNeedsLogDir(t *testing.T) {
	prepareCredentialFreeEnv(t)
	t.Setenv("FISH_DB_DSN", "")

	t.Setenv("FISH_LOG_DIR", "")
	if _, err := LoadForTail(writeLoaderConfig(t, "[log]\ndir=''\n"), ""); err == nil || !strings.Contains(err.Error(), "日志目录") {
		t.Fatalf("空日志目录应拒绝，实际 %v", err)
	}

	dir := t.TempDir()
	t.Setenv("FISH_LOG_DIR", dir)
	cfg, err := LoadForTail(writeLoaderConfig(t, credentialHeavyConfig()), "")
	if err != nil {
		t.Fatalf("tail 不应要求数据库或凭据：%v", err)
	}
	if cfg.Log.Dir != dir {
		t.Fatalf("日志目录解析错误：%q", cfg.Log.Dir)
	}
}

// TestLoadForCLIStillRejectsRealGiftWithoutCredential 真实发送命令仍严格校验凭据。
func TestLoadForCLIStillRejectsRealGiftWithoutCredential(t *testing.T) {
	prepareCredentialFreeEnv(t)
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")

	path := writeLoaderConfig(t, "[gift]\nenabled=true\nreal=true\n")
	if _, err := LoadForCLI(path, ""); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("真实送礼仍须校验凭据，实际 %v", err)
	}

	path = writeLoaderConfig(t, "[gift]\nenabled=true\nreal=false\n")
	if _, err := LoadForCLI(path, ""); err != nil {
		t.Fatalf("dry-run 不应要求凭据：%v", err)
	}
}

// TestReadOnlyLoadersIgnoreUnrelatedSectionErrors 只读诊断不校验它不使用的配置段。
func TestReadOnlyLoadersIgnoreUnrelatedSectionErrors(t *testing.T) {
	prepareCredentialFreeEnv(t)
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	// 非法礼物ID只影响送礼命令，不应挡住监控与诊断。
	body := "[gift]\nenabled=true\ngift_id='not-a-number'\n"

	if _, err := LoadForCLI(writeLoaderConfig(t, body), ""); err == nil || !strings.Contains(err.Error(), "礼物ID") {
		t.Fatalf("完整加载应拒绝非法礼物ID，实际 %v", err)
	}
	if _, err := LoadForStatus(writeLoaderConfig(t, body), ""); err != nil {
		t.Fatalf("status 不应校验无关的送礼配置：%v", err)
	}
	t.Setenv("FISH_LOG_DIR", t.TempDir())
	if _, err := LoadForTail(writeLoaderConfig(t, body), ""); err != nil {
		t.Fatalf("tail 不应校验无关的送礼配置：%v", err)
	}
	t.Setenv("FISH_DB_DSN", "")
	if _, err := LoadForWatchdog(writeLoaderConfig(t, body), ""); err != nil {
		t.Fatalf("看门狗不应校验无关的送礼配置：%v", err)
	}
}
