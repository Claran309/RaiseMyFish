package brain

// 本文件测直播间标题（LiveTitle）的注入与代播提示：
// 标题是直播间数据（谁在播），不是人格设定——只影响本轮的称呼判断。

import (
	"context"
	"strings"
	"testing"
)

// TestLiveTitleInjectedIntoPrompt 标题出现在 <live_title> 段。
func TestLiveTitleInjectedIntoPrompt(t *testing.T) {
	p := &fakeProvider{}
	b := makeBrain(t, p, &fakeUsage{}, options())

	task, in := fixture()
	in.LiveTitle = "本小姐今天种地！"
	if _, err := b.Reply(context.Background(), task, in); err != nil {
		t.Fatalf("Reply 失败：%v", err)
	}
	user := p.last().Messages[len(p.last().Messages)-1].Content
	if !strings.Contains(user, "<live_title>") || !strings.Contains(user, in.LiveTitle) {
		t.Fatalf("prompt 缺少 live_title 段：%s", user)
	}
	if strings.Contains(user, "别默认是主播自己") {
		t.Errorf("普通标题不该出现代播提示：%s", user)
	}
}

// TestLiveTitleStandInHint 标题暗示代播时给「别默认是主播自己」的提示。
func TestLiveTitleStandInHint(t *testing.T) {
	p := &fakeProvider{}
	b := makeBrain(t, p, &fakeUsage{}, options())

	task, in := fixture()
	in.LiveTitle = "今晚妈妈播~"
	if _, err := b.Reply(context.Background(), task, in); err != nil {
		t.Fatalf("Reply 失败：%v", err)
	}
	user := p.last().Messages[len(p.last().Messages)-1].Content
	if !strings.Contains(user, "今晚妈妈播~") || !strings.Contains(user, "别默认是主播自己") {
		t.Fatalf("代播标题缺少提示：%s", user)
	}
}

// TestLiveTitleEmptyOmitsSection 空标题不出现 live_title 段。
func TestLiveTitleEmptyOmitsSection(t *testing.T) {
	p := &fakeProvider{}
	b := makeBrain(t, p, &fakeUsage{}, options())

	task, in := fixture()
	if _, err := b.Reply(context.Background(), task, in); err != nil {
		t.Fatalf("Reply 失败：%v", err)
	}
	user := p.last().Messages[len(p.last().Messages)-1].Content
	if strings.Contains(user, "<live_title>") {
		t.Errorf("空标题不应出现 live_title 段：%s", user)
	}
}
