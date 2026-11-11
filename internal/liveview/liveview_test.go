package liveview

// 本文件测感知缓存：三路渲染、TTL 过期、清空与空缓存提示。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func at(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

func TestLiveViewRendersThreeSenses(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local)
	s := NewSenses(Options{TTL: time.Minute, Now: at(now)})
	s.UpdateFrame("主播在吃面包", now)
	s.UpdateAudio("今天中午吃的是面包", now)
	s.UpdateChat([]string{"观众A：饿了", "", "观众B：我也想"}, now)

	out, err := s.LiveView(context.Background())
	if err != nil {
		t.Fatalf("LiveView 失败：%v", err)
	}
	for _, want := range []string{
		"画面：主播在吃面包",
		"主播刚说：今天中午吃的是面包",
		"最近弹幕：",
		"- 观众A：饿了",
		"- 观众B：我也想",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("LiveView 缺少 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "观众A：饿了\n\n") {
		t.Errorf("空弹幕行未被过滤：\n%s", out)
	}
}

func TestLiveViewDropsStaleSenses(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local)
	clock := now
	s := NewSenses(Options{TTL: time.Minute, Now: func() time.Time { return clock }})
	s.UpdateFrame("旧画面", now)
	s.UpdateAudio("旧声音", now)
	clock = now.Add(2 * time.Minute)
	s.UpdateChat([]string{"观众A：还在吗"}, clock)

	out, err := s.LiveView(context.Background())
	if err != nil {
		t.Fatalf("LiveView 失败：%v", err)
	}
	if strings.Contains(out, "旧画面") || strings.Contains(out, "旧声音") {
		t.Fatalf("过期感知不应冒充现在：\n%s", out)
	}
	if !strings.Contains(out, "观众A：还在吗") {
		t.Errorf("新鲜弹幕应保留：\n%s", out)
	}
}

func TestUpdateAudioEmptyClearsStale(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local)
	s := NewSenses(Options{TTL: time.Minute, Now: at(now)})
	s.UpdateAudio("上一轮的声音", now)
	s.UpdateAudio("", now)

	out, err := s.LiveView(context.Background())
	if err != nil {
		t.Fatalf("LiveView 失败：%v", err)
	}
	if strings.Contains(out, "上一轮的声音") {
		t.Fatalf("ASR 失败后旧声音未被清空：\n%s", out)
	}
}

func TestLiveViewEmptyCache(t *testing.T) {
	s := NewSenses(Options{Now: at(time.Now())})
	out, err := s.LiveView(context.Background())
	if err != nil {
		t.Fatalf("LiveView 失败：%v", err)
	}
	if !strings.Contains(out, "还没采集到直播内容") {
		t.Fatalf("空缓存提示不符：%q", out)
	}
}

func TestLiveViewClipsSections(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local)
	s := NewSenses(Options{TTL: time.Minute, Now: at(now)})
	s.UpdateFrame(strings.Repeat("画", maxFrameRunes+50), now)
	s.UpdateAudio(strings.Repeat("声", maxAudioRunes+50), now)
	s.UpdateChat([]string{
		strings.Repeat("一", maxChatLineRunes+20),
		"第二条",
		"第三条",
		"第四条",
		"第五条",
	}, now)

	out, err := s.LiveView(context.Background())
	if err != nil {
		t.Fatalf("LiveView 失败：%v", err)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("超长段落未截断：\n%s", out)
	}
	if strings.Count(out, "\n- ") != maxChatLines {
		t.Errorf("弹幕行数未被限制为 %d：\n%s", maxChatLines, out)
	}
	if len([]rune(out)) > toolBudget+1 {
		t.Errorf("LiveView 超总预算：%d", len([]rune(out)))
	}
}

func TestLiveViewHonorsCanceledContext(t *testing.T) {
	s := NewSenses(Options{Now: at(time.Now())})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.LiveView(ctx); err == nil {
		t.Fatal("已取消的上下文应返回错误")
	}
}
