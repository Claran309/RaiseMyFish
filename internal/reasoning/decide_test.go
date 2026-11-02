package reasoning

import (
	"strings"
	"testing"
)

func TestDecideMemoryCues(t *testing.T) {
	for _, text := range []string{
		"你刚才说什么", "之前不是说过吗", "上次那个事", "上回你提的",
		"你说过什么", "你提过的设定", "你记得吗", "那个呢", "还记得吗", "又来了",
	} {
		got := Decide(text)
		if !got.NeedMemory || got.NeedKnowledge {
			t.Fatalf("%q 判断不符：%+v", text, got)
		}
		if !strings.Contains(got.Reason, "memory:") {
			t.Fatalf("%q 缺少命中原因：%q", text, got.Reason)
		}
	}
}

func TestDecideKnowledgeCues(t *testing.T) {
	for _, text := range []string{
		"这个梗是什么", "什么意思", "什么梗", "啥意思", "你知道吗", "这个梗怎么用",
	} {
		got := Decide(text)
		if got.NeedMemory || !got.NeedKnowledge {
			t.Fatalf("%q 判断不符：%+v", text, got)
		}
		if !strings.Contains(got.Reason, "knowledge:") {
			t.Fatalf("%q 缺少命中原因：%q", text, got.Reason)
		}
	}
}

// TestDecideGagCues 直播间常见梗词不带「梗」字也要触发知识召回。
func TestDecideGagCues(t *testing.T) {
	for _, cue := range []string{"拉拉", "给给", "何意味", "魔鱼", "队友呼吸机", "不烧心", "胆子肥嘟嘟"} {
		got := Decide("今天聊聊" + cue)
		if got.NeedMemory || !got.NeedKnowledge {
			t.Fatalf("梗词 %q 判断不符：%+v", cue, got)
		}
		if got.Reason != "knowledge:"+cue {
			t.Fatalf("梗词 %q 命中原因 = %q，期望 knowledge:%s", cue, got.Reason, cue)
		}
	}
}

func TestDecideBothCues(t *testing.T) {
	for _, text := range []string{
		"你还记得那个梗吗", "之前说的那个梗是什么意思", "那个梗",
	} {
		got := Decide(text)
		if !got.NeedMemory || !got.NeedKnowledge {
			t.Fatalf("%q 应同时命中记忆与知识：%+v", text, got)
		}
		if !strings.Contains(got.Reason, "memory:") || !strings.Contains(got.Reason, "knowledge:") {
			t.Fatalf("%q 命中原因不全：%q", text, got.Reason)
		}
	}
}

func TestDecideSimpleAndEmptyText(t *testing.T) {
	for _, text := range []string{"", "   ", "\t\n", "主播好", "今天鱼饲料不错", "哈哈哈", "？？？", "666"} {
		got := Decide(text)
		if got != (Decision{}) {
			t.Fatalf("%q 不应触发召回：%+v", text, got)
		}
	}
}

func TestDecideReasonIsStableAndDoesNotContainText(t *testing.T) {
	if got := Decide("刚才"); got.Reason != "memory:刚才" {
		t.Fatalf("单记忆命中原因 = %q", got.Reason)
	}
	if got := Decide("梗"); got.Reason != "knowledge:梗" {
		t.Fatalf("单知识命中原因 = %q", got.Reason)
	}
	if got := Decide("刚才的梗"); got.Reason != "memory:刚才;knowledge:梗" {
		t.Fatalf("双命中原因 = %q", got.Reason)
	}
	if got := Decide("主播好"); got.Reason != "" {
		t.Fatalf("未命中原因应为空：%q", got.Reason)
	}
	if got := Decide("刚才" + strings.Repeat("鱼", 200)); strings.Contains(got.Reason, "鱼") {
		t.Fatalf("命中原因不应包含原文：%q", got.Reason)
	}
}
