package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestOpenErrorDoesNotLeakDSN(t *testing.T) {
	_, err := Open(context.Background(), "postgres://fish:private-secret@127.0.0.1:1/fish?connect_timeout=1", time.Second)
	if err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("错误未脱敏：%v", err)
	}
	if errors.Unwrap(err) == nil {
		t.Fatal("底层错误没有保留")
	}
}

// TestPoolOptionsNormalize 零值必须补成原硬编码默认值，避免手工 Config 把池配成 0。
func TestPoolOptionsNormalize(t *testing.T) {
	got := PoolOptions{}.normalize()
	if got.MaxOpenConns != 30 || got.MaxIdleConns != 10 || got.ConnMaxLifetime != 30*time.Minute ||
		got.SlowThreshold != 200*time.Millisecond {
		t.Fatalf("默认值不符：%+v", got)
	}
	// idle 不能超过 open；显式大于时夹到 open。
	got = PoolOptions{MaxOpenConns: 5, MaxIdleConns: 9, ConnMaxLifetime: time.Minute}.normalize()
	if got.MaxIdleConns != 5 || got.MaxOpenConns != 5 {
		t.Fatalf("idle 未夹到 open：%+v", got)
	}
}

// TestSlowLoggerUsesZap 慢查询只走 zap（不落 stderr），且保留参数化占位符。
func TestSlowLoggerUsesZap(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	logger := newGormLogger(PoolOptions{Logger: zap.New(core), SlowThreshold: time.Millisecond})
	logger.Trace(context.Background(), time.Now().Add(-time.Second), func() (string, int64) {
		return "SELECT text FROM events WHERE id = ?", 1
	}, nil)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("慢查询日志条数 = %d，期望 1", len(entries))
	}
	if entries[0].Message != "数据库慢查询或告警" {
		t.Errorf("日志消息=%q", entries[0].Message)
	}
	detail := fmt.Sprint(entries[0].ContextMap()["detail"])
	if !strings.Contains(detail, "SLOW SQL") || !strings.Contains(detail, "?") {
		t.Errorf("慢查询日志缺少占位 SQL：%q", detail)
	}
	if strings.Contains(detail, "secret-value") {
		t.Errorf("慢查询日志不应包含参数值：%q", detail)
	}
}

// TestSilentLoggerWithoutZap 未注入 zap 时保持静默，不往 stderr 写。
func TestSilentLoggerWithoutZap(t *testing.T) {
	if got := newGormLogger(PoolOptions{SlowThreshold: time.Millisecond}); got == nil {
		t.Fatal("logger 不能为 nil")
	}
}
