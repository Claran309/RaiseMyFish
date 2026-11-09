package main

// 本文件测 fishwatchdog 入口：配置开关、必填校验、可正常启动并响应取消。

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"RaiseMyFish/internal/config"
)

func writeWatchdogConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunExitsWhenDisabled(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	path := writeWatchdogConfig(t, "[watchdog]\nenabled=false\n")
	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"-config", path, "-env-file", ""}, &out, &errOut); err != nil {
		t.Fatalf("未启用应正常退出：%v (stderr=%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "未启用") {
		t.Errorf("stdout 未提示未启用：%q", out.String())
	}
}

func TestRunRequiresRestartCommand(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	path := writeWatchdogConfig(t, "[watchdog]\nenabled=true\n")
	var out, errOut bytes.Buffer
	err := run(context.Background(), []string{"-config", path, "-env-file", ""}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "restart_command") {
		t.Fatalf("缺 restart_command 应拒绝启动，实际 %v", err)
	}
}

func TestRunStartsAndStopsOnCancel(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	heartbeat := filepath.ToSlash(filepath.Join(t.TempDir(), "fishd.heartbeat"))
	body := "[watchdog]\n" +
		"enabled=true\n" +
		"interval_seconds=60\n" +
		"stale_seconds=120\n" +
		"unhealthy_threshold=2\n" +
		"cooldown_seconds=300\n" +
		"restart_command='echo restart'\n" +
		"restart_mode='service-command'\n" +
		"heartbeat_path='" + heartbeat + "'\n"
	path := writeWatchdogConfig(t, body)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if err := run(ctx, []string{"-config", path, "-env-file", ""}, &out, &errOut); err != nil {
		t.Fatalf("取消后应正常退出：%v (stderr=%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "看门狗启动") {
		t.Errorf("stdout 未提示启动：%q", out.String())
	}
}

// TestRunStartsWithDBOnlyAccountsAndNoCookies N17：服务能跑时，看门狗不能因
// 环境没有平台 cookie 而启动失败；它只需要 [watchdog] 自身配置。
func TestRunStartsWithDBOnlyAccountsAndNoCookies(t *testing.T) {
	for _, key := range []string{"FISH_BILI_COOKIE", "FISH_BILI_COOKIE_FILE", "FISH_BILI_SELF_ID"} {
		t.Setenv(key, "")
	}
	// 显式空环境会覆盖 TOML，因此房间号与 LLM 端点按配置保持一致。
	t.Setenv("FISH_BILI_ROOM_ID", "1")
	t.Setenv("FISH_LLM_KEY", "")
	t.Setenv("FISH_LLM_BASE_URL", "http://127.0.0.1:18080/v1")
	t.Setenv("FISH_LLM_MODEL", "m")
	t.Setenv("FISH_DB_DSN", "") // 看门狗不连数据库

	heartbeat := filepath.ToSlash(filepath.Join(t.TempDir(), "fishd.heartbeat"))
	body := `[llm]
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
heartbeat_path='` + heartbeat + `'
`
	path := writeWatchdogConfig(t, body)

	// 同一份配置走完整加载会因平台凭据缺失被拒，证明场景成立。
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	if _, err := config.Load(path, ""); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("完整加载应因缺少B站凭据失败，实际 %v", err)
	}
	// 看门狗不连数据库。
	t.Setenv("FISH_DB_DSN", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if err := run(ctx, []string{"-config", path, "-env-file", ""}, &out, &errOut); err != nil {
		t.Fatalf("只有 DB 账号、环境无 cookie 时看门狗应能启动：%v (stderr=%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "看门狗启动") {
		t.Fatalf("stdout 未提示启动：%q", out.String())
	}
}
