package memory

// 本文件测焦点轮次（B1/N02）：独立窗口、可选来源、越界拒绝与 HybridSearcher 转发。

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

// fakeFocusSource 在三段查询之外多实现 FocusMessages，记录实际收到的请求。
type fakeFocusSource struct {
	fakeRecordSource

	messages []Message
	err      error
	calls    int
	got      FocusQuery
}

func (f *fakeFocusSource) FocusMessages(_ context.Context, q FocusQuery) ([]Message, error) {
	f.calls++
	f.got = q
	if f.err != nil {
		return nil, f.err
	}
	return f.messages, nil
}

// focusTurn 造一条属于 live/room-1 的双方对话消息。
func focusTurn(id string, dir event.Dir, fromID, fromName, text, replyTo string, at time.Time) Message {
	return Message{
		EventID: id, Channel: event.ChLive, Session: "room-1", Dir: dir,
		FromID: fromID, FromName: fromName, Text: text, ReplyTo: replyTo, At: at,
	}
}

func focusTurns() []Message {
	return []Message{
		focusTurn("live:room-1:q1", event.DirIn, "u-1", "阿问", "我是谁", "", readerBaseTime),
		focusTurn("live:out:room-1:r1", event.DirOut, "9", "本鱼", "你是阿问", "live:room-1:q1", readerBaseTime.Add(time.Second)),
	}
}

// TestFocusUsesOwnWindow 焦点窗口独立生效，Speaker 去空白、limit 按 FocusLimit 下发。
func TestFocusUsesOwnWindow(t *testing.T) {
	src := &fakeFocusSource{messages: focusTurns()}
	r, err := NewReader(src, nil, ReaderOptions{FocusLimit: 6})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	got, err := r.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "  u-1  "})
	if err != nil {
		t.Fatalf("Focus 失败：%v", err)
	}
	if len(got) != 2 || got[1].ReplyTo != "live:room-1:q1" {
		t.Fatalf("焦点轮次不符：%+v", got)
	}
	if src.got.Speaker != "u-1" || src.got.Scope != liveScope() || src.got.Limit != 6 {
		t.Fatalf("查询参数不符：%+v", src.got)
	}

	// 请求里的 limit 覆盖默认值，并夹到上限。
	if _, err := r.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "u-1", Limit: 999}); err != nil {
		t.Fatalf("Focus 失败：%v", err)
	}
	if src.got.Limit != maxReaderLimit {
		t.Fatalf("limit 未夹到上限：%d", src.got.Limit)
	}
	if _, err := r.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "u-1", Limit: -1}); err != nil {
		t.Fatalf("Focus 失败：%v", err)
	}
	if src.got.Limit != 6 {
		t.Fatalf("负数 limit 应回落默认 %d，实际 %d", 6, src.got.Limit)
	}
}

// TestFocusDefaultLimit 零值 ReaderOptions 使用 defaultFocusLimit。
func TestFocusDefaultLimit(t *testing.T) {
	src := &fakeFocusSource{}
	r, _ := NewReader(src, nil, ReaderOptions{})
	if _, err := r.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "u-1"}); err != nil {
		t.Fatalf("Focus 失败：%v", err)
	}
	if src.got.Limit != defaultFocusLimit {
		t.Fatalf("默认焦点窗口 = %d，期望 %d", src.got.Limit, defaultFocusLimit)
	}
}

// TestFocusUnsupportedSource 来源不支持焦点查询 → 显式空结果，不报错。
func TestFocusUnsupportedSource(t *testing.T) {
	r, _ := NewReader(&fakeRecordSource{}, nil, ReaderOptions{})
	got, err := r.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "u-1"})
	if err != nil || got != nil {
		t.Fatalf("不支持的来源应返回 nil,nil：got=%+v err=%v", got, err)
	}
}

// TestFocusRejectsInvalidInput 非法参数/越界数据必须在进入 Brain 前被拒绝。
func TestFocusRejectsInvalidInput(t *testing.T) {
	if _, err := NewReader(&fakeFocusSource{}, nil, ReaderOptions{}); err != nil {
		t.Fatal(err)
	}
	src := &fakeFocusSource{}
	r, _ := NewReader(src, nil, ReaderOptions{})

	if _, err := r.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "   "}); err == nil {
		t.Fatal("空 speaker 应报错")
	}
	if _, err := r.Focus(context.Background(), FocusQuery{Scope: Scope{Channel: "dm", Session: "u-9"}, Speaker: "u-1"}); err == nil {
		t.Fatal("非法 Scope 应报错")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Focus(ctx, FocusQuery{Scope: liveScope(), Speaker: "u-1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，实际 %v", err)
	}
	if src.calls != 0 {
		t.Fatalf("非法参数不应触发查询，实际 %d 次", src.calls)
	}

	// 查询失败必须透传，不能伪装成没有对话。
	srcErr := &fakeFocusSource{err: errFake}
	rErr, _ := NewReader(srcErr, nil, ReaderOptions{})
	if _, err := rErr.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "u-1"}); !errors.Is(err, errFake) {
		t.Fatalf("错误未透传：%v", err)
	}

	// 来源返回跨会话/私密越界消息 → 二次校验拦下。
	srcLeak := &fakeFocusSource{messages: []Message{{
		EventID: "live:other:q1", Channel: event.ChLive, Session: "other", Dir: event.DirIn,
		Text: "别人的会话", At: readerBaseTime,
	}}}
	rLeak, _ := NewReader(srcLeak, nil, ReaderOptions{})
	if _, err := rLeak.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "u-1"}); err == nil {
		t.Fatal("跨会话焦点消息必须报错")
	}

	srcPrivate := &fakeFocusSource{messages: []Message{{
		EventID: "live:room-1:q1", Channel: event.ChLive, Session: "room-1", Dir: event.DirIn,
		Text: "私密行", Private: true, At: readerBaseTime,
	}}}
	rPrivate, _ := NewReader(srcPrivate, nil, ReaderOptions{})
	if _, err := rPrivate.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "u-1"}); err == nil {
		t.Fatal("公开范围出现私密焦点消息必须报错")
	}
}

// focusSearchBase 在既有召回桩上多实现 Focus，用来验证 HybridSearcher 的转发。
type focusSearchBase struct {
	fakeSearchBase

	messages []Message
	calls    int
	got      FocusQuery
}

func (f *focusSearchBase) Focus(_ context.Context, q FocusQuery) ([]Message, error) {
	f.calls++
	f.got = q
	return f.messages, nil
}

// TestHybridSearchForwardsFocus HybridSearcher 必须把焦点查询原样转给底层 Reader。
func TestHybridSearchForwardsFocus(t *testing.T) {
	base := &focusSearchBase{messages: focusTurns()}
	h, err := NewHybridSearcher(base, &fakeSearchSource{}, nil, HybridSearchOptions{Alpha: 0.5})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	got, err := h.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "u-1", Limit: 4})
	if err != nil {
		t.Fatalf("Focus 失败：%v", err)
	}
	if base.calls != 1 || base.got.Speaker != "u-1" || len(got) != 2 {
		t.Fatalf("未转发焦点查询：calls=%d got=%+v", base.calls, base.got)
	}

	// 底层不支持焦点时返回空结果，不影响其他召回。
	plain, err := NewHybridSearcher(&fakeSearchBase{}, &fakeSearchSource{}, nil, HybridSearchOptions{Alpha: 0.5})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if got, err := plain.Focus(context.Background(), FocusQuery{Scope: liveScope(), Speaker: "u-1"}); err != nil || got != nil {
		t.Fatalf("底层不支持时应返回 nil,nil：got=%+v err=%v", got, err)
	}
}
