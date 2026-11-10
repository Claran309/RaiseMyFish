package sender

import (
	"RaiseMyFish/internal/event"
	"encoding/json"
	"testing"
)

func TestLiveWireFor(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		meta       string
		fromID     string
		fromName   string
		wantText   string
		wantMID    string
		wantName   string
	}{
		{"别人@鱼时回给发送者", "@弓长三益 收到", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "收到", "123", "弓长三益"},
		{"回复其他观众", "@弓长三益 你说得对", `{"reply_to_id":"999","reply_to_name":"弓长三益"}`, "123", "路人", "你说得对", "999", "弓长三益"},
		{"全角@", "＠弓长三益 好", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "好", "123", "弓长三益"},
		{"正文没有@前缀", "收到", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "收到", "", ""},
		{"@鱼自己不产生目标", "@大肥鱼 我", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "@大肥鱼 我", "", ""},
		{"更长昵称不算命中", "@弓长三益二号 好", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "@弓长三益二号 好", "", ""},
		{"损坏Meta", "@弓长三益 好", `{`, "123", "弓长三益", "@弓长三益 好", "", ""},
		{"昵称含控制字符", "@坏\x01蛋 好", `{"reply_to_id":"8"}`, "123", "坏\x01蛋", "@坏\x01蛋 好", "", ""},
		{"剥空保留原文", "@弓长三益", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "@弓长三益", "123", "弓长三益"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := event.Event{Channel: event.ChLive, FromID: tc.fromID, FromName: tc.fromName, Meta: json.RawMessage(tc.meta)}
			wire := LiveWireFor(e, tc.text, "8")
			if wire.Text != tc.wantText || wire.ReplyMID != tc.wantMID || wire.ReplyName != tc.wantName {
				t.Fatalf("快照=%q/%q/%q，期望=%q/%q/%q", wire.Text, wire.ReplyMID, wire.ReplyName, tc.wantText, tc.wantMID, tc.wantName)
			}
		})
	}
}

func TestStripMentionPrefixWhenStructuredMention(t *testing.T) {
	cases := []struct{ text, name, want string }{
		{"@Claran309 你好呀", "Claran309", "你好呀"},
		{"＠Claran309  在吗  ", "Claran309", "在吗"},
		{"你好呀", "Claran309", "你好呀"},
		{"@Claran309", "Claran309", "@Claran309"},         // 剥空保留原文
		{"@Claran3099 你好", "Claran309", "@Claran3099 你好"}, // 不误剥相似昵称
		{"@Claran309 你好", "", "@Claran309 你好"},            // 无昵称不剥
	}
	for _, tc := range cases {
		if got := stripMentionPrefix(tc.text, tc.name); got != tc.want {
			t.Errorf("stripMentionPrefix(%q, %q) = %q, 期望 %q", tc.text, tc.name, got, tc.want)
		}
	}
}
