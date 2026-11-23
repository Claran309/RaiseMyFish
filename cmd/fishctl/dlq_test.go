package main

import (
	"bytes"
	"context"
	"testing"
)

func TestDLQUsageValidation(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runDLQ(context.Background(), "", "", nil, &out, &errOut); err == nil {
		t.Fatal("缺少子命令未拒绝")
	}
	if err := runDLQ(context.Background(), "", "", []string{"unknown"}, &out, &errOut); err == nil {
		t.Fatal("未知子命令未拒绝")
	}
	if err := runDLQ(context.Background(), "", "", []string{"replay"}, &out, &errOut); err == nil {
		t.Fatal("缺少 --id 未拒绝")
	}
}
