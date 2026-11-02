package proactive

import (
	"strings"
	"testing"
)

func TestSanitizeDanmaku(t *testing.T) {
	cases := map[string]string{
		"弹幕：\"鱼鱼好可爱！\"\n解释：这是观众弹幕": "鱼鱼好可爱！",
		"「今天的鱼好精神」":                "今天的鱼好精神",
		"  一\n二  ":                 "一",
		"😄😄":                       "",
		"这是一句超过十五个字的观众弹幕内容":        "这是一句超过十五个字的观众弹幕",
	}
	for input, want := range cases {
		if got := sanitizeDanmaku(input); got != want {
			t.Errorf("sanitizeDanmaku(%q)=%q，期望 %q", input, got, want)
		}
	}
	if got := []rune(sanitizeDanmaku("一二三四五六七八九十一二三四五六七八")); len(got) != MaxDanmakuRunes {
		t.Errorf("超长弹幕未截断到15字：%q", string(got))
	}
}

func TestSimilarityAndRecent(t *testing.T) {
	if got := similarity("鱼鱼，好可爱！", "鱼鱼好可爱"); got != 1 {
		t.Errorf("仅标点差异应视为相同，实际 %v", got)
	}
	if got := similarity("今天鱼鱼很精神", "完全不同的内容"); got >= similarityThreshold {
		t.Errorf("无关文本相似度过高：%v", got)
	}
	if got := similarity("鱼鱼好可爱", "鱼鱼好可爱啊"); got < similarityThreshold {
		t.Errorf("近似文本应命中阈值：%v", got)
	}

	r := newRecent(2)
	if r.similar("鱼鱼好可爱") {
		t.Fatal("空历史不应命中")
	}
	r.add("鱼鱼好可爱")
	r.add("今天状态不错")
	r.add("第三条")
	if len(r.texts) != 2 || r.texts[0] != "今天状态不错" || r.texts[1] != "第三条" {
		t.Fatalf("最近列表未按上限裁剪：%+v", r.texts)
	}
	if !r.similar("第三条！") {
		t.Fatal("仍在窗口内的近似文本应命中")
	}
	if r.similar("鱼鱼好可爱！") {
		t.Fatal("被挤出窗口的文本不应再命中")
	}
	if !hasText("666") || hasText("！？。") {
		t.Fatal("hasText 判断错误")
	}
	if strings.TrimSpace(sanitizeDanmaku("\x00\x01")) != "" {
		t.Fatal("控制字符应被清空")
	}
}
