package main

// 本文件测 fishwatchdog 入口：配置开关、必填校验、可正常启动并响应取消。

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
