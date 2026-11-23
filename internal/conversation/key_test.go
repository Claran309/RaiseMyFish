package conversation

import (
	"strings"
	"testing"

	"RaiseMyFish/internal/event"
)

func TestKeyByChannel(t *testing.T) {
	dm := event.Event{Channel: event.ChDM, Session: "1001:2002", Private: true}
	if got := Key(dm); got != "dm:1001:2002" {
		t.Fatalf("私信会话键=%q", got)
	}
	legacy := event.Event{Channel: event.ChDM, Session: "2002", Private: true}
	if got := Key(legacy); got != "dm:legacy:2002" {
		t.Fatalf("旧私信会话键=%q", got)
	}
	top := event.Event{Channel: event.ChComment, Session: "1700001", ReplyTo: "555"}
	if got := Key(top); got != "comment:1700001:555" {
		t.Fatalf("顶层评论会话键=%q", got)
	}
	thread := event.Event{Channel: event.ChComment, Session: "1700001", ReplyTo: "999", Meta: []byte(`{"root":"555"}`)}
	if got := Key(thread); got != "comment:1700001:555" {
		t.Fatalf("楼中楼会话键=%q", got)
	}
	live := event.Event{Channel: event.ChLive, Session: "30308220"}
	if got := Key(live); got != "" {
		t.Fatalf("直播不应建持久会话：%q", got)
	}
}

// TestKeyIsolatesVideoAndPeer 不同视频/不同 peer 不能共享会话。
func TestKeyIsolatesVideoAndPeer(t *testing.T) {
	a := event.Event{Channel: event.ChComment, Session: "1700001", ReplyTo: "555"}
	b := event.Event{Channel: event.ChComment, Session: "1700002", ReplyTo: "555"}
	if Key(a) == Key(b) {
		t.Fatal("跨视频共享了会话键")
	}
	p1 := event.Event{Channel: event.ChDM, Session: "1001:2002", Private: true}
	p2 := event.Event{Channel: event.ChDM, Session: "1001:3003", Private: true}
	if Key(p1) == Key(p2) {
		t.Fatal("不同 peer 共享了私信会话键")
	}
}

// TestShortHidesIdentifiers 日志短标识不含 UID/正文。
func TestShortHidesIdentifiers(t *testing.T) {
	key := "dm:1001:2002"
	short := Short(key)
	if short == "" || strings.Contains(short, "1001") || strings.Contains(short, "2002") {
		t.Fatalf("短标识泄漏 ID：%q", short)
	}
	if Short(key) != short {
		t.Fatal("短标识不稳定")
	}
}
