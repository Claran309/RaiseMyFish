package main

import (
	"strings"
	"testing"

	"RaiseMyFish/internal/event"
)

// TestShouldUseReActShortPath L3：短评论/私信走单步；长文仍走 ReAct；0 关闭。
func TestShouldUseReActShortPath(t *testing.T) {
	if shouldUseReActWithShortPath(event.ChComment, "谢谢", true, 30) {
		t.Fatal("短评论应走单步")
	}
	if !shouldUseReActWithShortPath(event.ChComment, strings.Repeat("长", 31), true, 30) {
		t.Fatal("长评论应走 ReAct")
	}
	if !shouldUseReActWithShortPath(event.ChDM, "谢谢", true, 0) {
		t.Fatal("阈值 0 应保持旧行为")
	}
	if !shouldUseReActWithShortPath(event.ChLive, "谢谢", true, 30) {
		t.Fatal("live 不受快慢双路径影响")
	}
}
