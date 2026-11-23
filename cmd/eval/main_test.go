package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestEvalFixtureEndToEnd 走真实仓库文件：人格 fixture、坏回复、检索各出一份产物。
func TestEvalFixtureEndToEnd(t *testing.T) {
	root := filepath.Join("..", "..")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	out := filepath.Join(t.TempDir(), "eval")
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	if err := run(context.Background(), []string{"--output", out}, stdout); err != nil {
		t.Fatalf("评测失败：%v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary Summary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.Passed || summary.Persona == nil || summary.BadReplies == nil || summary.Knowledge == nil {
		t.Fatalf("总览不完整：%+v", summary)
	}
	if summary.Persona.Total != 35 || !summary.Persona.Complete || summary.Persona.SourceKind != "test" {
		t.Fatalf("人格 fixture 异常：%+v", summary.Persona)
	}
	if !summary.BadReplies.Passed || summary.BadReplies.Detected != summary.BadReplies.Total {
		t.Fatalf("坏回复检测异常：%+v", summary.BadReplies)
	}
	for _, file := range []string{"summary.md", "bad-replies.json", "persona/report.json", "knowledge/report.md"} {
		if _, err := os.Stat(filepath.Join(out, file)); err != nil {
			t.Fatalf("缺少产物 %s：%v", file, err)
		}
	}
}

// TestEvalRejectsExistingOutput 不允许覆盖旧产物。
func TestEvalRejectsExistingOutput(t *testing.T) {
	out := t.TempDir()
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	if err := run(context.Background(), []string{"--output", out}, stdout); err == nil {
		t.Fatal("已存在目录未被拒绝")
	}
}
