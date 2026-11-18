package main

// 本文件测 ASR 守护：只做探测与按冷却拉起，不真正启动外部进程。

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestASRSupervisorStartsWithCooldownAndRecovers(t *testing.T) {
	now := time.Date(2026, 11, 18, 22, 0, 0, 0, time.Local)
	checks, starts := 0, 0
	s := &asrSupervisor{
		endpoint: "http://127.0.0.1:8077/v1/audio/transcriptions",
		interval: time.Second,
		cooldown: time.Minute,
		log:      zap.NewNop(),
		now:      func() time.Time { return now },
		check: func(context.Context) error {
			checks++
			if checks < 4 {
				return errors.New("ASR服务不可达")
			}
			return nil
		},
		start: func(context.Context) error {
			starts++
			return nil
		},
	}
	ctx := context.Background()

	// 第一次探测失败：应拉起一次。
	s.checkOnce(ctx)
	if starts != 1 || s.available {
		t.Fatalf("首次不可用应拉起且状态不可用：starts=%d available=%v", starts, s.available)
	}
	// 冷却期内继续失败：不重复拉起。
	s.checkOnce(ctx)
	if starts != 1 {
		t.Fatalf("冷却期内重复拉起：%d", starts)
	}
	// 冷却结束后仍失败：再拉起一次。
	now = now.Add(time.Minute + time.Second)
	s.checkOnce(ctx)
	if starts != 2 {
		t.Fatalf("冷却结束后未重新拉起：%d", starts)
	}
	// 恢复可用：不再拉起。
	s.checkOnce(ctx)
	if starts != 2 || !s.available {
		t.Fatalf("恢复后状态错误：starts=%d available=%v", starts, s.available)
	}
}

func TestASRSupervisorStartErrorDoesNotPanic(t *testing.T) {
	s := &asrSupervisor{
		endpoint: "http://127.0.0.1:8077/v1/audio/transcriptions",
		interval: time.Second,
		cooldown: time.Minute,
		log:      zap.NewNop(),
		now:      time.Now,
		check:    func(context.Context) error { return errors.New("down") },
		start:    func(context.Context) error { return errors.New("启动脚本失败") },
	}
	s.checkOnce(context.Background())
	if s.available {
		t.Fatal("启动失败不应标记为可用")
	}
}
