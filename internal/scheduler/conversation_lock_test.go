package scheduler

import (
	"testing"

	"RaiseMyFish/internal/event"
)

// TestSessionPrefersConversationID C6：有会话键时按会话串行，不同会话不互相阻塞。
func TestSessionPrefersConversationID(t *testing.T) {
	a := event.Event{Channel: event.ChDM, Session: "1001:2002", ConversationID: "dm:1001:2002"}
	b := event.Event{Channel: event.ChDM, Session: "1001:2002", ConversationID: "dm:1001:2002"}
	c := event.Event{Channel: event.ChDM, Session: "1001:3003", ConversationID: "dm:1001:3003"}
	if session(a) != session(b) {
		t.Fatal("同一会话未共享锁键")
	}
	if session(a) == session(c) {
		t.Fatal("不同会话被错误合并")
	}
	// 无会话键时回退旧键，保证兼容。
	legacy := event.Event{Channel: event.ChDM, Session: "1001:2002"}
	if session(legacy) != (sessionKey{channel: event.ChDM, session: "1001:2002"}) {
		t.Fatalf("回退键不符：%+v", session(legacy))
	}
}
