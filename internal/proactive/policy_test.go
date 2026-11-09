package proactive

// 本文件测 N22 的纯策略：观察语义指纹、对话新颖性、追问/纠正让路、四态分类。

import (
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

func testMessage(id string, dir event.Dir, text string, at time.Time) memory.Message {
	return memory.Message{
		EventID: id, Channel: event.ChLive, Session: "30308220",
		Dir: dir, Text: text, At: at,
	}
}

func TestShingleOverlap(t *testing.T) {
	sameSceneA := "主播坐在桌前打游戏，右下角弹幕不停滚动"
	sameSceneB := "主播坐在桌前玩着游戏，右下角弹幕还在滚动"
	otherScene := "窗外下着小雨，一只黑猫趴在窗台上看风景。"

	if got := shingleOverlap(sameSceneA, sameSceneB); got < viewpointSimilarityThreshold {
		t.Errorf("同一画面的不同措辞应命中观点去重，实际 %v", got)
	}
	if got := shingleOverlap(sameSceneA, otherScene); got >= viewpointSimilarityThreshold {
		t.Errorf("不同画面不应命中观点去重，实际 %v", got)
	}
	if got := shingleOverlap("好", "好"); got != 0 {
		t.Errorf("不足两个字符没有二元组，应为0，实际 %v", got)
	}

	if similarity(sameSceneA, sameSceneB) >= observationSimilarityThreshold {
		t.Fatalf("测试前提不成立：两种措辞应被判定为画面有变化")
	}
	if !recentSemanticDuplicate("这界面有点东西", "这界面有点内容") {
		t.Error("语义近似文本应命中已说过去重")
	}
	if recentSemanticDuplicate("主播这波操作真秀", "窗外黑猫在看雨") {
		t.Error("无关文本不应命中语义去重")
	}
}

func recentSemanticDuplicate(a, b string) bool {
	r := newRecent(2)
	r.add(a)
	return r.similar(b)
}

func TestConversationFromOnlyTracksInbound(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	inbound := testMessage("in1", event.DirIn, "在吗", base)
	outbound := testMessage("out1", event.DirOut, "在", base.Add(10*time.Second))

	view := conversationFrom(memory.Context{Recent: []memory.Message{inbound}})
	if !strings.Contains(view.key, "in1") {
		t.Fatalf("对话 key 应绑定最新入向消息：%q", view.key)
	}
	if len(view.pending) != 1 {
		t.Fatalf("未回答的问题应进入待让路列表：%+v", view.pending)
	}
	if reason := view.yieldReason(base.Add(10 * time.Second)); reason != ReasonYieldFollowUp {
		t.Errorf("应识别为追问，实际 %q", reason)
	}

	// 鱼自己的出站消息不能改变对话 key，否则每轮主动弹幕都会把「自己刚说过」
	// 当成新事实，重新开闸。
	outbound.ReplyTo = inbound.EventID
	withOut := conversationFrom(memory.Context{Recent: []memory.Message{inbound, outbound}})
	if withOut.key != view.key {
		t.Errorf("出站消息不应改变对话 key：%q vs %q", withOut.key, view.key)
	}
	if len(withOut.pending) != 0 {
		t.Errorf("ReplyTo 指向本条后不应继续让路：%+v", withOut.pending)
	}

	// 新的入向消息才改变 key。
	newer := testMessage("in2", event.DirIn, "所以呢", base.Add(20*time.Second))
	changed := conversationFrom(memory.Context{Recent: []memory.Message{inbound, newer}})
	if changed.key == view.key {
		t.Error("新的入向消息应改变对话 key")
	}
}

func TestConversationFromDedupsFocusAndSorts(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	older := testMessage("in1", event.DirIn, "刚才说到哪了", base)
	newer := testMessage("in2", event.DirIn, "你继续啊", base.Add(30*time.Second))
	view := conversationFrom(memory.Context{
		Recent: []memory.Message{newer, older},
		Focus:  []memory.Message{older, newer},
	})
	if !strings.Contains(view.key, "in2") {
		t.Errorf("最新入向消息应为 in2：%q", view.key)
	}
	for _, message := range view.pending {
		if message.EventID == "" {
			t.Errorf("pending 不应包含空消息：%+v", view.pending)
		}
	}
	if len(view.pending) != 2 {
		t.Errorf("Recent/Focus 重复消息应去重：%+v", view.pending)
	}
}

func TestYieldReason(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		text   string
		at     time.Time
		now    time.Time
		reason string
	}{
		{"问号追问", "刚才那是什么意思？", base.Add(-10 * time.Second), base, ReasonYieldFollowUp},
		{"句尾吗", "你看到了吗", base.Add(-10 * time.Second), base, ReasonYieldFollowUp},
		{"继续追问", "所以呢", base.Add(-10 * time.Second), base, ReasonYieldFollowUp},
		{"纠正", "你刚才说错了，不是那样", base.Add(-10 * time.Second), base, ReasonYieldCorrection},
		{"旧消息不让路", "在吗", base.Add(-3 * time.Minute), base, ""},
		{"普通聊天不让路", "今天状态不错", base.Add(-10 * time.Second), base, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := testMessage("in", event.DirIn, tc.text, tc.at)
			view := conversationFrom(memory.Context{Recent: []memory.Message{message}})
			if got := view.yieldReason(tc.now); got != tc.reason {
				t.Errorf("yieldReason(%q)=%q，期望 %q", tc.text, got, tc.reason)
			}
		})
	}
}

func TestAnsweredUsesReplyToEvidence(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	asked := testMessage("in1", event.DirIn, "在吗", base)
	otherReply := testMessage("out1", event.DirOut, "这鱼真肥", base.Add(5*time.Second))
	otherReply.ReplyTo = "someone-else"
	view := conversationFrom(memory.Context{Recent: []memory.Message{asked, otherReply}})
	if len(view.pending) != 1 {
		t.Fatalf("回复了别人不应算回答本条追问：%+v", view.pending)
	}

	answer := testMessage("out2", event.DirOut, "在", base.Add(10*time.Second))
	answer.ReplyTo = "in1"
	view = conversationFrom(memory.Context{Recent: []memory.Message{asked, answer}})
	if len(view.pending) != 0 {
		t.Fatalf("ReplyTo 指到本条应算已回答：%+v", view.pending)
	}
}

func TestClassifyAction(t *testing.T) {
	if got := classifyAction("大家吃了吗？", true); got != ActionAsk {
		t.Errorf("问句应为 ask：%q", got)
	}
	if got := classifyAction("哈哈这也太逗了", true); got != ActionReact {
		t.Errorf("画面有变化应为 react：%q", got)
	}
	if got := classifyAction("原来是这样", false); got != ActionComment {
		t.Errorf("画面没变化、对话有变化应为 comment：%q", got)
	}
}
