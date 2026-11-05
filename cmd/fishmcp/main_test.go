package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunRequiresEnabled 未启用时直接报错，且 stdout 不能有任何协议外输出。
func TestRunRequiresEnabled(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private@localhost/fish")
	p := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(p, []byte("[mcp]\nenabled=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	err := run(context.Background(), []string{"--config", p, "--env-file", ""}, strings.NewReader(""), &stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("未启用时应报错：%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout 必须保持干净，实际：%q", stdout.String())
	}
}

// TestRunRejectsArgs fishmcp 不接受子命令。
func TestRunRejectsArgs(t *testing.T) {
	err := run(context.Background(), []string{"serve"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "用法") {
		t.Fatalf("多余参数应被拒绝：%v", err)
	}
}
