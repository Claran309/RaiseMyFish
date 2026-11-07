//go:build integration

package main

// T25：真实 PG + 假客户端（合成直播子进程）。
// 环境没有B站 cookie/self_id，仅 DB 活跃账号提供凭据时 fishd 仍能完成装配并采集。

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/account"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/infra"
)

const t25DBCookie = "SESSDATA=db-t25; bili_jct=db-t25-csrf"

func TestFishdStartsWithDBActiveAccountOnly(t *testing.T) {
	dsn := knowledgeCheckTestDSN(t)
	t.Setenv("FISH_DB_DSN", dsn)
	for _, key := range []string{"FISH_BILI_COOKIE", "FISH_BILI_COOKIE_FILE", "FISH_BILI_SELF_ID"} {
		t.Setenv(key, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(db); err != nil {
			t.Error(err)
		}
	}()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo, err := database.NewAccountRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Upsert(ctx, account.Account{
		ID: account.IDForMID("8"), MID: "8", Name: "T25",
		Cookie: t25DBCookie, CSRF: "db-t25-csrf", Status: account.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "child-started")
	script := filepath.Join(dir, "fake-live.mjs")
	source := fmt.Sprintf(`import fs from 'node:fs';
const cookie = process.env.FISH_BILI_COOKIE || '';
if (!cookie.includes('SESSDATA=db-t25')) process.exit(21);
fs.writeFileSync(%q, 'ok');
const emit = frame => process.stdout.write(JSON.stringify({ protocol: 1, ...frame }) + '\n');
emit({ type: 'ready', room_id: '99', self_id: '8' });
emit({ type: 'event', room_id: '99', msg_id: '9007199254740993123', from_id: '7', from_name: '观众', text: 'DB账号合成消息', at: '2026-11-01T08:00:00Z' });
setInterval(() => emit({ type: 'heartbeat' }), 100);
`, filepath.ToSlash(marker))
	if err := os.WriteFile(script, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "logs")
	statePath := filepath.Join(dir, "status.json")
	cfgPath := filepath.Join(dir, "fishd.toml")
	text := fmt.Sprintf("[log]\ndir=%q\n[runtime]\nstate_file=%q\nheartbeat_seconds=1\nstale_seconds=5\n[watchdog]\nheartbeat_path=%q\n[channels.live]\nenabled=true\n[collector.live]\nroom_id='1'\nscript_path=%q\n",
		filepath.ToSlash(logDir), filepath.ToSlash(statePath), filepath.ToSlash(filepath.Join(dir, "heartbeat")), filepath.ToSlash(script))
	if err := os.WriteFile(cfgPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}

	base := []string{"--config", cfgPath, "--env-file", ""}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- run(runCtx, base, io.Discard, io.Discard) }()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int64
		if err := db.Table("events").Where("id=?", "live:99:9007199254740993123").Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("fishd 在采集到事件前退出：%v", err)
		case <-ctx.Done():
			t.Fatal("等待 DB 账号启动采集超时")
		case <-ticker.C:
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("假客户端未收到 DB 凭据：%v", err)
	}
	stop()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("取消后 fishd 未退出")
	}
	state, err := infra.ReadStatus(statePath)
	if err != nil || state.State != "stopped" {
		t.Fatalf("退出后状态不符：%+v err=%v", state, err)
	}
	assertNoCredentialInLogs(t, logDir)
}

// TestFishdRejectsWhenNoDBCredential 环境与 DB 都没有凭据时，必须在启动期明确拒绝。
func TestFishdRejectsWhenNoDBCredential(t *testing.T) {
	dsn := knowledgeCheckTestDSN(t)
	t.Setenv("FISH_DB_DSN", dsn)
	for _, key := range []string{"FISH_BILI_COOKIE", "FISH_BILI_COOKIE_FILE", "FISH_BILI_SELF_ID"} {
		t.Setenv(key, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(db); err != nil {
			t.Error(err)
		}
	}()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "fishd.toml")
	t.Setenv("FISH_LOG_DIR", filepath.Join(dir, "logs"))
	t.Setenv("FISH_STATE_FILE", filepath.Join(dir, "status.json"))
	if err := os.WriteFile(cfgPath, []byte("[channels.live]\nenabled=true\n[collector.live]\nroom_id='1'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err = run(ctx, []string{"--config", cfgPath, "--env-file", "", "check"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("没有可用账号时应拒绝启动，实际 %v", err)
	}
}

// assertNoCredentialInLogs 日志不得出现 DB cookie 原文。
func assertNoCredentialInLogs(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), t25DBCookie) {
			t.Fatalf("日志泄露B站凭据：%s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("读取日志失败：%v", err)
	}
}
