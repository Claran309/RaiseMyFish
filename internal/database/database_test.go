package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
