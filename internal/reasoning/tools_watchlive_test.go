package reasoning

// 本文件测批 H 的 watch_live 工具：仅在有感知缓存依赖时注册，只读、截断、错误透传。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeLiveView struct {
	out   string
	err   error
	calls int
}

func (f *fakeLiveView) LiveView(_ context.Context) (string, error) {
	f.calls++
	return f.out, f.err
}

// TestWatchLiveRegisteredWithSource 注入 LiveView 时额外注册 watch_live，
// 描述里带「画面/主播/弹幕」关键词，结果按只读工具统一口径截断。
func TestWatchLiveRegisteredWithSource(t *testing.T) {
	d := readonlyDeps()
	view := &fakeLiveView{out: "画面：桌上有面包\n主播刚说：好饿\n最近弹幕：甲：吃了吗"}
	d.LiveView = view
	box, err := BuildReadOnlyTools(d)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if !box.Has(ToolWatchLive) {
		t.Fatalf("注入 LiveView 后应注册 watch_live：%v", box.Names())
	}
	desc := box.Descriptions()
	if !strings.Contains(desc, ToolWatchLive) || !strings.Contains(desc, "画面") || !strings.Contains(desc, "弹幕") {
		t.Errorf("watch_live 描述不完整：%q", desc)
	}

	got, err := box.Run(context.Background(), ToolWatchLive, "")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if got != view.out || view.calls != 1 {
		t.Errorf("结果不符：got=%q calls=%d", got, view.calls)
	}
}

// TestWatchLiveArgsIgnored 协议约定 args 可忽略：传了也无害，不校验不报错。
func TestWatchLiveArgsIgnored(t *testing.T) {
	d := readonlyDeps()
	view := &fakeLiveView{out: "感知"}
	d.LiveView = view
	box, _ := BuildReadOnlyTools(d)

	if _, err := box.Run(context.Background(), ToolWatchLive, `{"foo":"bar"}`); err != nil {
		t.Fatalf("args 应被忽略：%v", err)
	}
	if view.calls != 1 {
		t.Errorf("工具未执行：%d", view.calls)
	}
}

// TestWatchLiveTruncates 感知结果超过 500 字时截断，避免撑爆 ReAct 观察段。
func TestWatchLiveTruncates(t *testing.T) {
	d := readonlyDeps()
	d.LiveView = &fakeLiveView{out: strings.Repeat("鱼", 800)}
	box, _ := BuildReadOnlyTools(d)

	got, err := box.Run(context.Background(), ToolWatchLive, "")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if n := len([]rune(got)); n > maxReadOnlyRunes+1 {
		t.Errorf("结果未截断：%d rune", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("截断后应带省略号")
	}
}

// TestWatchLiveAbsentWithoutSource 被动回复没有感知缓存：不注册（而不是注册后报未配置），
// 避免模型在普通弹幕里调用一个永远没有数据的工具。
func TestWatchLiveAbsentWithoutSource(t *testing.T) {
	box, err := BuildReadOnlyTools(readonlyDeps())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if box.Has(ToolWatchLive) {
		t.Fatalf("未注入 LiveView 不应注册 watch_live：%v", box.Names())
	}
	for _, name := range box.Names() {
		if name == ToolWatchLive {
			t.Error("工具列表出现了 watch_live")
		}
	}
	if _, err := box.Run(context.Background(), ToolWatchLive, ""); err == nil {
		t.Error("未注册的工具应执行失败")
	}
}

// TestWatchLiveErrorPropagates 感知缓存读取失败向上返回，由 ReAct 记成一条观察。
func TestWatchLiveErrorPropagates(t *testing.T) {
	d := readonlyDeps()
	d.LiveView = &fakeLiveView{err: errors.New("缓存未就绪")}
	box, _ := BuildReadOnlyTools(d)

	if _, err := box.Run(context.Background(), ToolWatchLive, ""); err == nil || !strings.Contains(err.Error(), "缓存未就绪") {
		t.Errorf("错误应透传：%v", err)
	}
}
