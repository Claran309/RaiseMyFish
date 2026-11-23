package main

import (
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/conversation"
	"RaiseMyFish/internal/event"
)

// TestLiveWindowOnlyWhenEnabled C3：只有 enabled + live_window_seconds>0 才建窗口。
func TestLiveWindowOnlyWhenEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Conversation
		want bool
	}{
		{"默认关闭", config.Conversation{Enabled: false, LiveWindowSeconds: 30}, false},
		{"窗口为0", config.Conversation{Enabled: true, LiveWindowSeconds: 0}, false},
		{"开启", config.Conversation{Enabled: true, LiveWindowSeconds: 30}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveWindowFrom(tc.cfg) != nil; got != tc.want {
				t.Fatalf("窗口=%v，期望 %v", got, tc.want)
			}
		})
	}
}

// TestLiveWindowInputInjectsRecentTurns 30 秒内追问能接上，超时不注入。
func TestLiveWindowInputInjectsRecentTurns(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	window := conversation.NewWindow(conversation.WindowOptions{TTL: 30 * time.Second, Now: clock})
	cfg := config.Conversation{Enabled: true, LiveWindowSeconds: 30}
	first := event.Event{ID: "live:room:1", Channel: event.ChLive, Dir: event.DirIn, Session: "room", FromID: "42", FromName: "阿明", Text: "这个好看"}
	window.Add(first)
	now = now.Add(5 * time.Second)
	got := conversationInput(t.Context(), nil, cfg, window, event.Event{
		ID: "live:room:2", Channel: event.ChLive, Dir: event.DirIn, Session: "room", FromID: "42", Text: "多少钱",
	}, nil)
	if got == nil || len(got.Turns) != 1 || got.Turns[0].Text != "这个好看" || got.Turns[0].From != "阿明" {
		t.Fatalf("短窗口未注入：%+v", got)
	}
	now = now.Add(31 * time.Second)
	if got := conversationInput(t.Context(), nil, cfg, window, event.Event{
		ID: "live:room:3", Channel: event.ChLive, Dir: event.DirIn, Session: "room", FromID: "42", Text: "还在吗",
	}, nil); got != nil {
		t.Fatalf("超时仍注入：%+v", got)
	}
}

// TestLiveWindowDoesNotAffectOtherChannels 关闭直播窗口时私信/评论行为不变。
func TestLiveWindowDoesNotAffectOtherChannels(t *testing.T) {
	cfg := config.Conversation{Enabled: true, DMEnabled: true, LiveWindowSeconds: 0}
	if got := conversationInput(t.Context(), nil, cfg, nil, event.Event{
		ID: "dm:1:2:1", Channel: event.ChDM, Session: "1:2", Private: true,
	}, nil); got != nil {
		t.Fatalf("没有仓库时私信不应凭空造上下文：%+v", got)
	}
	if got := conversationInput(t.Context(), nil, config.Conversation{}, nil, event.Event{
		ID: "live:room:1", Channel: event.ChLive, Session: "room", FromID: "42",
	}, nil); got != nil {
		t.Fatalf("会话总开关关闭时仍注入：%+v", got)
	}
}

// TestConversationRolloutIsStableAndPerChannel C2：灰度按会话键稳定，
// 且关闭评论灰度不影响私信。
func TestConversationRolloutIsStableAndPerChannel(t *testing.T) {
	cfg := config.Conversation{
		Enabled: true, DMEnabled: true, CommentEnabled: true,
		DMRolloutPercent: 100, CommentRolloutPercent: 0,
	}
	dm := event.Event{ID: "dm:1:2:1", Channel: event.ChDM, Session: "1:2", Private: true, ConversationID: "dm:1:2"}
	if !inConversationRollout(cfg, dm) {
		t.Fatal("私信 100% 灰度未命中")
	}
	comment := event.Event{ID: "comment:1700001:555:1", Channel: event.ChComment, Session: "1700001", ReplyTo: "555"}
	if inConversationRollout(cfg, comment) {
		t.Fatal("评论 0% 灰度仍命中")
	}
	// 同一 thread 的楼中楼必须与根评论同结论（键相同）。
	cfg.CommentRolloutPercent = 50
	thread := event.Event{Channel: event.ChComment, Session: "1700001", ReplyTo: "999", Meta: []byte(`{"root":"555"}`)}
	first := inConversationRollout(cfg, comment)
	for i := 0; i < 10; i++ {
		if inConversationRollout(cfg, thread) != first {
			t.Fatal("同一 thread 灰度结论不稳定")
		}
	}
	// 直播不受 rollout 影响（走短窗口）。
	if inConversationRollout(cfg, event.Event{Channel: event.ChLive, Session: "room", FromID: "42"}) {
		t.Fatal("直播不该走持久会话灰度")
	}
}
