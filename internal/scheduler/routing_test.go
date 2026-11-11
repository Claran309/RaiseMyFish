package scheduler

// 本文件测 live 弹幕筛选路由：规则命中、纯闲聊跳过，以及 @ 回带目标。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

// TestFilterRelevantRules 非 @ 但相关（叫名字/提问/请求/上下文指代）放行，纯闲聊不放行。
func TestFilterRelevantRules(t *testing.T) {
	policy := ChannelPolicy{
		SelfNames: []string{"大肥鱼"},
		SelfIDs:   []string{"3747574135655903"},
		OwnerIDs:  []string{"313591854"},
		Whitelist: []string{"开饭啦"},
	}
	base := claimFor("live:r:1", "r", "今天天气真不错", time.Now()).Event
	base.FromID = "viewer-1"
	base.FromName = "路人"

	cases := []struct {
		name string
		mut  func(*event.Event)
		want bool
	}{
		{"直接@", func(e *event.Event) { e.Text = "@大肥鱼 在吗" }, true},
		{"叫名字", func(e *event.Event) { e.Text = "大肥鱼唱首歌吧" }, true},
		{"结构化回复", func(e *event.Event) {
			e.Text = "宝宝，摸摸你的尾巴"
			e.Meta = json.RawMessage(`{"reply_to_id":"3747574135655903"}`)
		}, true},
		{"白名单", func(e *event.Event) { e.Text = "开饭啦" }, true},
		{"主人发言", func(e *event.Event) { e.FromID = "313591854" }, true},
		{"提问", func(e *event.Event) { e.Text = "这个副本怎么打" }, true},
		{"请求", func(e *event.Event) { e.Text = "帮我讲个笑话" }, true},
		{"上下文指代", func(e *event.Event) { e.Text = "刚才说的那个继续" }, true},
		{"纯闲聊", func(e *event.Event) { e.Text = "哈哈哈哈" }, false},
		{"普通陈述", func(e *event.Event) { e.Text = "今天天气真不错" }, false},
		{"@别人", func(e *event.Event) { e.Text = "@小黑 你好" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := base
			if tc.mut != nil {
				tc.mut(&ev)
			}
			if got := filterRelevant(ev, policy); got != tc.want {
				t.Errorf("filterRelevant(%q)=%v，期望 %v", ev.Text, got, tc.want)
			}
		})
	}
	// 其他通道不受弹幕筛选影响。
	comment := base
	comment.Channel = event.ChComment
	comment.Text = "哈哈哈哈"
	if !filterRelevant(comment, policy) {
		t.Error("非 live 通道不应走弹幕筛选")
	}
}

// TestRoutingSkipModes 三种模式的准入判定互不串味。
func TestRoutingSkipModes(t *testing.T) {
	ev := claimFor("live:r:1", "r", "这个怎么打", time.Now()).Event
	if routingSkip(ev, ChannelPolicy{RoutingMode: RoutingFilter, SelfNames: []string{"大肥鱼"}}) {
		t.Error("filter 模式应放行相关问题")
	}
	if !routingSkip(ev, ChannelPolicy{RoutingMode: RoutingMentionOnly, SelfNames: []string{"大肥鱼"}}) {
		t.Error("mention_only 模式应拦下未 @ 弹幕")
	}
	if routingSkip(ev, ChannelPolicy{RoutingMode: RoutingAll}) {
		t.Error("all 模式不应拦截")
	}
	// 旧配置没有 RoutingMode：按 ReplyOnlyMention 推导。
	if !routingSkip(ev, ChannelPolicy{ReplyOnlyMention: true, SelfNames: []string{"大肥鱼"}}) {
		t.Error("旧 reply_only_mention=true 应等同 mention_only")
	}
	if routingSkip(ev, ChannelPolicy{ReplyOnlyMention: false}) {
		t.Error("旧 reply_only_mention=false 应等同 all")
	}
}

// TestSchedulerFilterRouteSkipsChatBeforeAdmission filter 模式在准入前跳过纯闲聊，
// 相关问题即使没有 @ 也进入 Admit。
func TestSchedulerFilterRouteSkipsChatBeforeAdmission(t *testing.T) {
	s, b, r, clock := fixture(t)
	p := s.policies[event.ChLive]
	p.RoutingMode = RoutingFilter
	p.SelfNames = []string{"大肥鱼"}
	p.SelfIDs = []string{"3747574135655903"}
	p.OwnerIDs = []string{"owner-1"}
	// fixture 的并发是 4；本轮要同时放 5 条相关弹幕。
	s.options.MaxConcurrent = 8

	now := clock.Now()
	direct := claimFor("live:a:direct", "a", "@大肥鱼 在吗", now)
	named := claimFor("live:b:named", "b", "大肥鱼唱首歌吧", now)
	question := claimFor("live:c:question", "c", "这个副本怎么打", now)
	owner := claimFor("live:d:owner", "d", "晚安", now)
	owner.Event.FromID = "owner-1"
	structured := claimFor("live:e:structured", "e", "宝宝，摸摸你的尾巴", now)
	structured.Event.Meta = json.RawMessage(`{"reply_to_id":"3747574135655903","reply_to_name":"大肥鱼"}`)
	chat := claimFor("live:f:chat", "f", "哈哈哈哈", now)
	statement := claimFor("live:g:statement", "g", "今天天气真不错", now)

	b.add(direct, named, question, owner, structured, chat, statement)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []event.Claim{direct, named, question, owner, structured} {
		if !strings.Contains(strings.Join(r.admitted, ","), ev.Event.ID) {
			t.Errorf("相关弹幕未进入 Admit：%s（%v）", ev.Event.ID, r.admitted)
		}
	}
	for _, ev := range []event.Claim{chat, statement} {
		if b.marked(ev.Event.ID) != event.StatusSkipped {
			t.Errorf("纯闲聊未在准入前跳过：%s", ev.Event.ID)
		}
		if strings.Contains(strings.Join(r.admitted, ","), ev.Event.ID) {
			t.Errorf("纯闲聊不应进入 Admit：%s", ev.Event.ID)
		}
	}
	for i := 0; i < 5; i++ {
		drainDone(t, s)
	}
}

// TestMentionBackTargetPlainMention 普通 @ 弹幕（无结构化 Meta）也要 @ 回发送者。
func TestMentionBackTargetPlainMention(t *testing.T) {
	policy := ChannelPolicy{ReplyMentionBack: true, SelfNames: []string{"大肥鱼"}}
	ev := claimFor("live:r:1", "r", "@大肥鱼 在吗", time.Now()).Event
	ev.FromName = "路人"
	if got, ok := MentionBackTarget(ev, policy); !ok || got != "路人" {
		t.Fatalf("普通 @ 应 @ 发送者，实际 (%q,%v)", got, ok)
	}

	ev.FromName = ""
	if _, ok := MentionBackTarget(ev, policy); ok {
		t.Fatal("发送者昵称为空时不应加前缀")
	}

	// @ 别人不算指向鱼。
	ev.FromName = "路人"
	ev.Text = "@小黑 你好"
	if _, ok := MentionBackTarget(ev, policy); ok {
		t.Fatal("@ 别人不应触发回带")
	}
}
