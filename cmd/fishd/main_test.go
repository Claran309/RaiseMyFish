package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"RaiseMyFish/internal/infra"
	"go.uber.org/zap"
)

func TestServeCancellationAndFinalState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	path := filepath.Join(t.TempDir(), "status.json")
	calls := 0
	check := func(context.Context) error {
		calls++
		if calls == 2 {
			cancel()
		}
		return nil
	}
	if err := serve(ctx, path, time.Millisecond, zap.NewNop(), check); err != nil {
		t.Fatal(err)
	}
	state, err := infra.ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "stopped" || state.PID <= 0 {
		t.Fatal("取消后未落停止状态")
	}
}

func TestServeDatabaseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	calls := 0
	check := func(context.Context) error {
		calls++
		if calls > 1 {
			return errors.New("数据库断开")
		}
		return nil
	}
	if err := serve(context.Background(), path, time.Millisecond, zap.NewNop(), check); err == nil {
		t.Fatal("数据库故障未退出")
	}
	state, err := infra.ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "stopped" {
		t.Fatal("故障后仍显示运行")
	}
}

func TestRunRejectsUnknownCommandAndMissingConfig(t *testing.T) {
	if err := run(context.Background(), []string{"unknown"}, io.Discard, io.Discard); err == nil {
		t.Fatal("接受未知命令")
	}
	if err := run(context.Background(), []string{"--config", filepath.Join(t.TempDir(), "missing")}, io.Discard, io.Discard); err == nil {
		t.Fatal("接受缺失配置")
	}
}
