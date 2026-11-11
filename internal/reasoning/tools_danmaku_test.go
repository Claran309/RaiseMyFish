package reasoning

// 本文件测 send_danmaku 工具：单轮多条、去重、长度/单行校验、失败不计成功与日志。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// fakeDanmakuSender 记录实际发出的弹幕；fail 非空时每次发送都失败。
type fakeDanmakuSender struct {
	mu     sync.Mutex
	texts  []string
	fail   error
	failed int
}

func (f *fakeDanmakuSender) SendDanmaku(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		f.failed++
		return f.fail
	}
	f.texts = append(f.texts, text)
	return nil
}

func (f *fakeDanmakuSender) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

// steppingClock 每次取时间都前进一秒，绕开默认 800ms 限频。
func steppingClock() func() time.Time {
	now := time.Unix(1000, 0)
	return func() time.Time {
		now = now.Add(time.Second)
		return now
	}
}

func TestDanmakuToolSendsMultipleWithGuards(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	sender := &fakeDanmakuSender{}
	box, err := BuildDanmakuTool(DanmakuDeps{Sender: sender, Now: steppingClock(), Log: zap.New(core)})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	ctx := context.Background()
	for _, args := range []string{
		`{"text":"第一条"}`,
		"第二条", // 宽松输入：参数本身就是正文
		`{"text":"第三条"}`,
	} {
		if _, err := box.Run(ctx, ToolSendDanmaku, args); err != nil {
			t.Fatalf("第 %q 条应发送成功：%v", args, err)
		}
	}
	if got := sender.sent(); len(got) != 3 || got[0] != "第一条" || got[2] != "第三条" {
		t.Fatalf("发出的弹幕不符：%+v", got)
	}

	cases := []struct {
		name string
		args string
		want string
	}{
		{"超过单轮上限", `{"text":"第四条"}`, "上限"},
		{"重复内容", `{"text":"第一条"}`, "已经发过"},
		{"超长", `{"text":"` + strings.Repeat("鱼", 41) + `"}`, "最多 40 字"},
		{"换行", `{"text":"上\n下"}`, "单行"},
		{"空正文", `{"text":"   "}`, "缺少 text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := box.Run(ctx, ToolSendDanmaku, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望含 %q 的错误，实际 %v", tc.want, err)
			}
		})
	}
	if got := sender.sent(); len(got) != 3 {
		t.Errorf("被拒绝的调用不应发出弹幕：%+v", got)
	}
	if logs.FilterMessage("弹幕工具调用").Len() != 8 {
		t.Errorf("每次调用都应记一条日志，实际 %d", logs.FilterMessage("弹幕工具调用").Len())
	}
	for _, entry := range logs.All() {
		if entry.ContextMap()["action"] != "tool_danmaku" {
			t.Errorf("日志 action 不符：%v", entry.ContextMap())
		}
		if strings.Contains(fmt.Sprint(entry.ContextMap()), "第一条") {
			t.Error("日志不应包含弹幕正文")
		}
	}
}

func TestDanmakuToolFailureKeepsRetryable(t *testing.T) {
	sender := &fakeDanmakuSender{fail: errors.New("网络断了")}
	box, err := BuildDanmakuTool(DanmakuDeps{Sender: sender, Now: steppingClock()})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	ctx := context.Background()
	if _, err := box.Run(ctx, ToolSendDanmaku, `{"text":"再来一条"}`); err == nil {
		t.Fatal("发送失败应返回错误")
	}
	// 失败不占用去重/条数：恢复后同一条仍可发。
	sender.mu.Lock()
	sender.fail = nil
	sender.mu.Unlock()
	if _, err := box.Run(ctx, ToolSendDanmaku, `{"text":"再来一条"}`); err != nil {
		t.Fatalf("失败后应允许重试同一条：%v", err)
	}
	if got := sender.sent(); len(got) != 1 || got[0] != "再来一条" {
		t.Fatalf("恢复后应只发出一次：%+v", got)
	}
}

func TestBuildDanmakuToolRequiresSender(t *testing.T) {
	if _, err := BuildDanmakuTool(DanmakuDeps{}); err == nil {
		t.Fatal("缺少执行器应报错")
	}
}
