package main

// 本文件测 fishd 心跳：启动即写、按间隔刷新、停止后不再写、空路径安全降级。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"RaiseMyFish/internal/watchdog"
	"go.uber.org/zap"
)

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时：%s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartHeartbeatWritesPIDAndRefreshes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "fishd.heartbeat")
	stop := startHeartbeatEvery(context.Background(), path, 20*time.Millisecond, zap.NewNop())
	defer stop()

	waitUntil(t, "心跳文件出现", func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var hb watchdog.Heartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		t.Fatalf("心跳不是合法JSON：%v", err)
	}
	if hb.PID != os.Getpid() || hb.At.IsZero() {
		t.Fatalf("心跳内容不符：%+v", hb)
	}

	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "心跳 mtime 刷新", func() bool {
		info, err := os.Stat(path)
		return err == nil && info.ModTime().After(first.ModTime())
	})
}

func TestStartHeartbeatStopHaltsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fishd.heartbeat")
	stop := startHeartbeatEvery(context.Background(), path, 10*time.Millisecond, zap.NewNop())
	waitUntil(t, "心跳文件出现", func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
	stop()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("stop 后仍写入了心跳")
	}
	stop() // 可重复调用
}

func TestStartHeartbeatEmptyPathIsNoop(t *testing.T) {
	stop := startHeartbeat(context.Background(), "", zap.NewNop())
	stop()
	if stop == nil {
		t.Fatal("应返回非 nil 的 stop")
	}
}
