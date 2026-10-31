//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/infra"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func TestRuntimeWithRealPostgres(t *testing.T) {
	dsn := os.Getenv("FISH_TEST_DSN")
	if dsn == "" {
		env, err := godotenv.Read(filepath.Join("..", "..", ".env"))
		if err != nil {
			t.Fatal("需要真实PG配置")
		}
		dsn = env["FISH_DB_DSN"]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := database.Open(ctx, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fish_app_%x", suffix)
	quoted := pgx.Identifier{schema}.Sanitize()
	if err := admin.WithContext(ctx).Exec("CREATE SCHEMA " + quoted).Error; err != nil {
		t.Fatal("创建测试schema失败")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if err := admin.WithContext(cleanup).Exec("DROP SCHEMA " + quoted + " CASCADE").Error; err != nil {
			t.Error("清理测试schema失败")
		}
		if err := database.Close(admin); err != nil {
			t.Error(err)
		}
	}()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("DSN格式错误")
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema
	}
	t.Setenv("FISH_DB_DSN", dsn)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "fishd.toml")
	statePath := filepath.Join(dir, "status.json")
	toml := fmt.Sprintf("[log]\ndir=%q\n[runtime]\nstate_file=%q\nheartbeat_seconds=1\nstale_seconds=5\n", filepath.ToSlash(filepath.Join(dir, "logs")), filepath.ToSlash(statePath))
	if err := os.WriteFile(configPath, []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--config", configPath, "--env-file", ""}
	for range 2 {
		args := append(append([]string{}, base...), "migrate", "up")
		if err := run(ctx, args, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	serviceCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- run(serviceCtx, base, io.Discard, io.Discard) }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := infra.ReadStatus(statePath)
		if err == nil && state.Alive(time.Now().UTC(), 5*time.Second) {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("服务提前退出：%v", err)
		case <-ctx.Done():
			t.Fatal("等待服务超时")
		case <-ticker.C:
		}
	}
	if err := run(ctx, base, io.Discard, io.Discard); err == nil {
		t.Fatal("第二实例未被拒绝")
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("取消后未退出")
	}
	state, err := infra.ReadStatus(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "stopped" {
		t.Fatal("退出后状态错误")
	}
}
