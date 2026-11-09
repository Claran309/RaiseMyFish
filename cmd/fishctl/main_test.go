package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/infra"
)

func TestTailReadsAppendAndCancels(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	path := filepath.Join(dir, "fishd-"+now.Format("2006-01-02")+".log")
	if err := os.WriteFile(path, []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &appendWriter{path: path, cancel: cancel}
	if err := tail(ctx, dir, w, func() time.Time { return now }, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if w.buf.String() != "first\nsecond\n" {
		t.Fatalf("跟随结果错误：%q", w.buf.String())
	}
}

type appendWriter struct {
	buf    bytes.Buffer
	path   string
	cancel context.CancelFunc
}

func (w *appendWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if bytes.Contains(w.buf.Bytes(), []byte("second\n")) {
		w.cancel()
		return n, err
	}
	f, e := os.OpenFile(w.path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return n, e
	}
	if _, e = f.WriteString("second\n"); e != nil {
		_ = f.Close()
		return n, e
	}
	return n, f.Close()
}

func TestStatusHonorsHeartbeatAndMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var out bytes.Buffer
	if err := printStatus(path, 20*time.Second, true, &out); err == nil {
		t.Fatal("缺状态却报在线")
	}
	state := infra.Status{PID: 1, State: "running", Database: "ready", UpdatedAt: time.Now().UTC()}
	if err := infra.WriteStatus(path, state); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := printStatus(path, 20*time.Second, true, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"service_alive":true`)) {
		t.Fatal("没有输出在线摘要")
	}
	if err := printStatus(path, 20*time.Second, false, io.Discard); err == nil {
		t.Fatal("数据库故障未影响状态")
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	if err := run(context.Background(), []string{"unknown"}, io.Discard, io.Discard); err == nil {
		t.Fatal("接受未知命令")
	}
}

type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(p []byte) (int, error) { return f(p) }

func TestTailSwitchesToNextDay(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	for day, text := range map[string]string{"2026-10-31": "day-one\n", "2026-11-01": "day-two\n"} {
		if err := os.WriteFile(filepath.Join(dir, "fishd-"+day+".log"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	w := writeFunc(func(p []byte) (int, error) {
		n, err := output.Write(p)
		if bytes.Contains(p, []byte("day-two")) {
			cancel()
		} else {
			now = now.Add(2 * time.Minute)
		}
		return n, err
	})
	if err := tail(ctx, dir, w, func() time.Time { return now }, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if output.String() != "day-one\nday-two\n" {
		t.Fatalf("跨日输出不正确：%q", output.String())
	}
}

func TestRunTailDoesNotRequireLiveDatabase(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private-secret@127.0.0.1:1/fish")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "fishd.toml")
	content := "[log]\ndir='" + filepath.ToSlash(dir) + "'\n"
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fishd-"+time.Now().Format("2006-01-02")+".log"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := writeFunc(func(p []byte) (int, error) { cancel(); return len(p), nil })
	if err := run(ctx, []string{"--config", configPath, "--env-file", "", "tail"}, w, io.Discard); err != nil {
		t.Fatal(err)
	}
}

// TestRunStatusWithoutLiveConfig status 不依赖 live 配置；数据库不可达时仍输出失败摘要。
func TestRunStatusWithoutLiveConfig(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private-secret@127.0.0.1:1/fish?connect_timeout=1")
	dir := t.TempDir()
	path := filepath.Join(dir, "fishd.toml")
	body := "[db]\ntimeout_seconds=1\n[runtime]\nstate_file=\"" +
		filepath.ToSlash(filepath.Join(dir, "status.json")) +
		"\"\n[channels.live]\nenabled=true\n[collector.live]\nroom_id=\"\"\nself_id=\"\"\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run(context.Background(), []string{"--config", path, "--env-file", "", "status"}, &out, io.Discard)
	if err == nil {
		t.Fatal("数据库不可达时应报错")
	}
	if strings.Contains(err.Error(), "直播采集") || strings.Contains(out.String(), "直播采集") {
		t.Fatalf("status 被 live 配置挡住：%v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"database_ready":false`)) {
		t.Fatalf("缺少数据库失败摘要：%s", out.String())
	}
}

func TestRunStatusReportsUnreachableDatabaseSafely(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private-secret@127.0.0.1:1/fish?connect_timeout=1")
	p := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(p, []byte("[db]\ntimeout_seconds=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run(context.Background(), []string{"--config", p, "--env-file", "", "status"}, &out, io.Discard)
	if err == nil || bytes.Contains(out.Bytes(), []byte("private-secret")) || bytes.Contains([]byte(err.Error()), []byte("private-secret")) {
		t.Fatal("失败状态或脱敏不正确")
	}
	if !bytes.Contains(out.Bytes(), []byte(`"database_ready":false`)) {
		t.Fatal("缺少数据库失败摘要")
	}
	var connectionFailure net.Error
	if !errors.As(err, &connectionFailure) {
		t.Fatal("连接失败原因未保留")
	}
}

// n17CredentialHeavyConfig 启用所有需要B站凭据的能力，模拟「只在 DB 存账号」的部署。
func n17CredentialHeavyConfig() string {
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
`
}

// prepareN17Env 固定「环境无 cookie」且 LLM 指向假端点。
func prepareN17Env(t *testing.T) {
	t.Helper()
	for _, key := range []string{"FISH_BILI_COOKIE", "FISH_BILI_COOKIE_FILE", "FISH_BILI_SELF_ID"} {
		t.Setenv(key, "")
	}
	t.Setenv("FISH_BILI_ROOM_ID", "1")
	t.Setenv("FISH_LLM_KEY", "")
	t.Setenv("FISH_LLM_BASE_URL", "http://127.0.0.1:18080/v1")
	t.Setenv("FISH_LLM_MODEL", "m")
}

// TestRunStatusWithDBOnlyAccountsAndNoCookies N17：status 不能被发送/采集凭据挡住。
func TestRunStatusWithDBOnlyAccountsAndNoCookies(t *testing.T) {
	prepareN17Env(t)
	t.Setenv("FISH_DB_DSN", "postgres://fish:private-secret@127.0.0.1:1/fish?connect_timeout=1")
	dir := t.TempDir()
	state := filepath.ToSlash(filepath.Join(dir, "status.json"))
	t.Setenv("FISH_STATE_FILE", state)
	body := n17CredentialHeavyConfig() +
		"[db]\ntimeout_seconds=1\n" +
		"[runtime]\nstate_file='" + state + "'\n"
	path := filepath.Join(dir, "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	// 同一份配置走完整加载会因缺少B站凭据被拒，证明场景成立。
	if _, err := config.Load(path, ""); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("完整加载应因缺少B站凭据失败，实际 %v", err)
	}

	var out bytes.Buffer
	err := run(context.Background(), []string{"--config", path, "--env-file", "", "status"}, &out, io.Discard)
	if err == nil {
		t.Fatal("数据库不可达时应报错")
	}
	if strings.Contains(err.Error(), "凭据") {
		t.Fatalf("status 被平台凭据挡住：%v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"database_ready":false`)) {
		t.Fatalf("缺少数据库失败摘要：%s", out.String())
	}
}

// TestRunTailWithDBOnlyAccountsAndNoCookies N17：tail 只需要日志目录，连库都不需要。
func TestRunTailWithDBOnlyAccountsAndNoCookies(t *testing.T) {
	prepareN17Env(t)
	t.Setenv("FISH_DB_DSN", "")
	dir := t.TempDir()
	t.Setenv("FISH_LOG_DIR", dir)
	body := n17CredentialHeavyConfig() + "[log]\ndir='" + filepath.ToSlash(dir) + "'\n"
	path := filepath.Join(dir, "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fishd-"+time.Now().Format("2006-01-02")+".log"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got bytes.Buffer
	w := writeFunc(func(p []byte) (int, error) {
		n, err := got.Write(p)
		cancel()
		return n, err
	})
	if err := run(ctx, []string{"--config", path, "--env-file", "", "tail"}, w, io.Discard); err != nil {
		t.Fatalf("只有 DB 账号、环境无 cookie 时 tail 应能启动：%v", err)
	}
	if got.String() != "one\n" {
		t.Fatalf("日志输出错误：%q", got.String())
	}
}
