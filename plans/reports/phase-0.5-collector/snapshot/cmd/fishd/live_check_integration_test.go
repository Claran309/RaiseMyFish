//go:build integration

package main

import (
	"RaiseMyFish/internal/database"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveCheckDoesNotStartProcessOrCallPlatform(t *testing.T) {
	dsn := knowledgeCheckTestDSN(t)
	t.Setenv("FISH_DB_DSN", dsn)
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=credential-canary")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn, time.Second)
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
	marker := filepath.Join(dir, "node-started")
	script := filepath.Join(dir, "touch.mjs")
	source := fmt.Sprintf("import fs from 'node:fs';fs.writeFileSync(%q,'started')", marker)
	if err := os.WriteFile(script, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FISH_LOG_DIR", filepath.Join(dir, "logs"))
	state := filepath.Join(dir, "state.json")
	t.Setenv("FISH_STATE_FILE", state)
	cfg := filepath.Join(dir, "check.toml")
	text := fmt.Sprintf("[channels.live]\nenabled=true\n[collector.live]\nroom_id='1'\nself_id='8'\nscript_path=%q\n", filepath.ToSlash(script))
	if err := os.WriteFile(cfg, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{"--config", cfg, "--env-file", "", "check"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Live     liveDiagnostic `json:"live_collector"`
		Provider bool           `json:"provider_called"`
		Usage    bool           `json:"usage_written"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Live.Enabled || !report.Live.Configured || report.Live.ProcessStarted || report.Live.PipelineConnected || report.Provider || report.Usage {
		t.Fatal("诊断不应启动实际链路")
	}
	for _, p := range []string{marker, state} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("check启动了进程或写status")
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), "credential-canary") {
		t.Fatal("诊断泄露凭据")
	}
	var count int64
	if err := db.Table("events").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("check不应采集事件", err)
	}
}

func TestFishdLiveIngestUsesConfiguredChildAndStops(t *testing.T) {
	dsn := knowledgeCheckTestDSN(t)
	t.Setenv("FISH_DB_DSN", dsn)
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=fixture-canary")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn, time.Second)
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
	t.Setenv("FISH_LOG_DIR", filepath.Join(dir, "logs"))
	state := filepath.Join(dir, "state.json")
	t.Setenv("FISH_STATE_FILE", state)
	script, err := filepath.Abs(filepath.Join("..", "..", "internal", "collector", "live", "testdata", "stream.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "live.toml")
	text := fmt.Sprintf("[channels.live]\nenabled=true\n[collector.live]\nroom_id='1'\nself_id='8'\nscript_path=%q\n", filepath.ToSlash(script))
	if err := os.WriteFile(cfg, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- run(runCtx, []string{"--config", cfg, "--env-file", ""}, io.Discard, io.Discard) }()
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
			t.Fatal("fishd采集提前结束", err)
		case <-ctx.Done():
			t.Fatal("fishd未采集到合成事件")
		case <-ticker.C:
		}
	}
	stop()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fishd取消未等待子进程")
	}
	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &status); err != nil || status.State != "stopped" {
		t.Fatal("基础设施没有优雅停止", err)
	}
	var count int64
	if err := db.Table("llm_usage").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("采集错误地调用了模型", err)
	}
}
