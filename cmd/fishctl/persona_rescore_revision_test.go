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

func TestRescoreCLIRevisionNeedsPinnedHash(t *testing.T) {
	args := []string{"--source", "../../plans/reports/persona-eval/20261031T143439Z-758949f2", "--output", filepath.Join(t.TempDir(), "revision"), "--rules-version", "persona-lexical-v3", "--cases-revision", "../../plans/reports/p0h-cases/cases-v3.jsonl"}
	if err := runPersonaRescore(context.Background(), args, io.Discard, io.Discard); err == nil {
		t.Fatal("没有hash也接受修订用例")
	}
	args = append(args, "--cases-sha256", "0acfa2afc3ed9bfce73e85a9abe479e2aee0f0e4d0301a019320853c3dea4670")
	var out bytes.Buffer
	if err := runPersonaRescore(context.Background(), args, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "actual_model_calls=0") || !strings.Contains(out.String(), "raw=15/35") {
		t.Fatal("未标注零调用及修订结果")
	}
	if _, err := os.Stat(filepath.Join(args[3], "cases-revision.jsonl")); err != nil {
		t.Fatal(err)
	}
}
