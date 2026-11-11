package proactive

// 本文件测批 G/H：主动弹幕的声音/最近弹幕采集、降级、感知缓存与 watch_live 主动读取。

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/reasoning"
	"go.uber.org/zap"
)

type fakeAudio struct {
	text  string
	err   error
	calls int
}

func (f *fakeAudio) Listen(context.Context) (string, error) {
	f.calls++
	return f.text, f.err
}

// blockingAudio 模拟录音一直不返回，直到上下文被取消（超时降级用例）。
type blockingAudio struct {
	started chan struct{}
	done    chan struct{}
}

func (b *blockingAudio) Listen(ctx context.Context) (string, error) {
	close(b.started)
	<-ctx.Done()
	close(b.done)
	return "", ctx.Err()
}

type fakeChat struct {
	rows     []event.Event
	err      error
	gotScope memory.Scope
	gotLimit int
	calls    int
}

func (f *fakeChat) RecentEvents(_ context.Context, scope memory.Scope, limit int) ([]event.Event, error) {
	f.calls++
	f.gotScope, f.gotLimit = scope, limit
	return f.rows, f.err
}

type fakeSenses struct {
	frame, audio string
	chat         []string
	at           time.Time
	frames       int
	audios       int
	chats        int
}

func (f *fakeSenses) UpdateFrame(text string, at time.Time) {
	f.frames++
	f.frame, f.at = text, at
}

func (f *fakeSenses) UpdateAudio(text string, at time.Time) {
	f.audios++
	f.audio, f.at = text, at
}

func (f *fakeSenses) UpdateChat(lines []string, at time.Time) {
	f.chats++
	f.chat, f.at = lines, at
}

// TestRunOnceInjectsAudioAndLiveChat 四感齐活：画面进 Visual，转写进 Audio，
// 最近 10 条（去自己/空文本、截断、正序）进 LiveChat，并写入感知缓存。
func TestRunOnceInjectsAudioAndLiveChat(t *testing.T) {
	f := newFixture(t)
	audio := &fakeAudio{text: "主播：今天这条鱼有点懒"}
	rows := []event.Event{
		{FromID: "999", FromName: "大肥鱼", Text: "我自己"},
		{FromID: "3", FromName: "丙", Text: "最近一条"},
		{FromID: "4", FromName: "", Text: "没名字"},
		{FromID: "5", FromName: "乙", Text: "   "},
		{FromID: "1", FromName: "甲", Text: "最早一条"},
		{FromID: "2", FromName: "丁", Text: strings.Repeat("长", 100)},
	}
	chat := &fakeChat{rows: rows}
	senses := &fakeSenses{}
	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, nil, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Audio: audio, Chat: chat, Senses: senses, SelfID: "999",
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if !got.Sent {
		t.Fatalf("应正常发送：%+v", got)
	}
	if audio.calls != 1 {
		t.Errorf("声音应采样一次：%d", audio.calls)
	}
	if chat.gotScope != (memory.Scope{Channel: event.ChLive, Session: "30308220"}) || chat.gotLimit != chatQueryLimit {
		t.Errorf("最近弹幕查询参数不符：scope=%+v limit=%d", chat.gotScope, chat.gotLimit)
	}
	if f.engine.gotInput.Audio != audio.text {
		t.Errorf("Audio 未注入：%q", f.engine.gotInput.Audio)
	}

	lines := strings.Split(f.engine.gotInput.LiveChat, "\n")
	want := []string{"丁：", "甲：最早一条", "没名字", "丙：最近一条"}
	if len(lines) != len(want) {
		t.Fatalf("最近弹幕行数 = %d，期望 %d：%q", len(lines), len(want), lines)
	}
	if lines[0] != want[0]+strings.Repeat("长", maxLiveChatLineRunes-2)+"…" {
		t.Errorf("长弹幕未按 %d 字截断：%q", maxLiveChatLineRunes, lines[0])
	}
	for i := 1; i < len(want); i++ {
		if lines[i] != want[i] {
			t.Errorf("第 %d 行 = %q，期望 %q", i, lines[i], want[i])
		}
	}
	if strings.Contains(f.engine.gotInput.LiveChat, "我自己") || strings.Contains(f.engine.gotInput.LiveChat, "大肥鱼") {
		t.Error("鱼自己的事件不应进入最近弹幕")
	}

	// 感知缓存：供 ReAct 的 watch_live 读取，时间取截帧时刻。
	if senses.frames != 1 || senses.frame != f.vision.text {
		t.Errorf("画面未写入缓存：%d %q", senses.frames, senses.frame)
	}
	if senses.audios != 1 || senses.audio != audio.text {
		t.Errorf("声音未写入缓存：%d %q", senses.audios, senses.audio)
	}
	if senses.chats != 1 || strings.Join(senses.chat, "\n") != f.engine.gotInput.LiveChat {
		t.Errorf("弹幕未写入缓存：%d %+v", senses.chats, senses.chat)
	}
	if !senses.at.Equal(*f.clock) {
		t.Errorf("缓存时间应为截帧时刻：%v vs %v", senses.at, *f.clock)
	}
}

// TestRunOnceAudioFailureDegrades ASR 失败只降级为无声音，并清掉缓存里的旧声音。
func TestRunOnceAudioFailureDegrades(t *testing.T) {
	f := newFixture(t)
	senses := &fakeSenses{audio: "上一轮的旧声音"}
	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, nil, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Audio: &fakeAudio{err: errors.New("asr down")}, Senses: senses,
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("ASR 失败不应阻塞本轮：%v", err)
	}
	if !got.Sent {
		t.Fatalf("应照常用画面/弹幕发送：%+v", got)
	}
	if f.engine.gotInput.Audio != "" {
		t.Errorf("ASR 失败时 Audio 应为空：%q", f.engine.gotInput.Audio)
	}
	if senses.audios != 1 || senses.audio != "" {
		t.Errorf("失败时应写入空声音清缓存：%d %q", senses.audios, senses.audio)
	}
	if senses.frame != f.vision.text {
		t.Error("声音失败不应影响画面缓存")
	}
}

// TestRunOnceAudioTimeoutDegrades 采样超过 AudioTimeout 时取消录音，本轮按无声音继续。
func TestRunOnceAudioTimeoutDegrades(t *testing.T) {
	f := newFixture(t)
	audio := &blockingAudio{started: make(chan struct{}), done: make(chan struct{})}
	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, nil, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Audio: audio, AudioTimeout: 40 * time.Millisecond, Senses: &fakeSenses{},
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	start := time.Now()
	got, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("超时不应阻塞本轮：%v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("超时降级耗时过长：%v", elapsed)
	}
	if !got.Sent || f.engine.gotInput.Audio != "" {
		t.Errorf("超时应降级为无声音并照常发送：%+v audio=%q", got, f.engine.gotInput.Audio)
	}
	select {
	case <-audio.done:
	case <-time.After(time.Second):
		t.Fatal("超时后录音 goroutine 未退出")
	}
}

// TestRunOnceChatFailureDegrades 最近弹幕读取失败只降级为空，不影响发送。
func TestRunOnceChatFailureDegrades(t *testing.T) {
	f := newFixture(t)
	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, nil, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Chat: &fakeChat{err: errors.New("db down")},
		Now:  func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("弹幕读取失败不应阻塞本轮：%v", err)
	}
	if !got.Sent || f.engine.gotInput.LiveChat != "" {
		t.Errorf("应降级为空并照常发送：%+v chat=%q", got, f.engine.gotInput.LiveChat)
	}
}

// TestRunOnceChatLimitedToTenLines 多取 20 条，过滤后只取最近 10 条，并按正序注入。
func TestRunOnceChatLimitedToTenLines(t *testing.T) {
	f := newFixture(t)
	rows := make([]event.Event, 0, 15)
	for i := 0; i < 15; i++ {
		rows = append(rows, event.Event{FromID: "u" + strconv.Itoa(i), FromName: "人" + strconv.Itoa(i), Text: "第" + strconv.Itoa(i) + "条"})
	}
	chat := &fakeChat{rows: rows}
	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, nil, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220", Chat: chat,
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	lines := strings.Split(f.engine.gotInput.LiveChat, "\n")
	if len(lines) != maxLiveChatLines {
		t.Fatalf("应只取最近 %d 条：%d", maxLiveChatLines, len(lines))
	}
	// rows 是倒序（新→旧），注入后应反转为正序（旧→新）。
	if lines[0] != "人9：第9条" || lines[len(lines)-1] != "人0：第0条" {
		t.Errorf("最近弹幕顺序不符：first=%q last=%q", lines[0], lines[len(lines)-1])
	}
	if chat.gotLimit != chatQueryLimit {
		t.Errorf("应多取 %d 条再过滤：%d", chatQueryLimit, chat.gotLimit)
	}
}

// TestNewRejectsNegativeAudioTimeout 负的声音等待超时是配置错误，构造期拒绝。
func TestNewRejectsNegativeAudioTimeout(t *testing.T) {
	f := newFixture(t)
	if _, err := New(f.snap, f.vision, f.engine, testPack(), nil, nil, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220", AudioTimeout: -time.Second,
	}); err == nil {
		t.Error("负 AudioTimeout 应报错")
	}
}

// sequenceLLM 按顺序回放脚本，并保留第一次调用的 prompt（反思不会覆盖）。
type sequenceLLM struct {
	replies []string
	calls   int

	firstSystem string
	firstUser   string
}

func (s *sequenceLLM) Complete(_ context.Context, system, user string) (string, error) {
	s.calls++
	if s.calls == 1 {
		s.firstSystem, s.firstUser = system, user
	}
	idx := s.calls - 1
	if idx < len(s.replies) {
		return s.replies[idx], nil
	}
	return "", errors.New("脚本用尽")
}

// reactWithWatchLive 造一个注册了 watch_live 的 ReAct 工厂，并记录工具调用次数。
func reactWithWatchLive(t *testing.T, llm *sequenceLLM, calls *int) ReactFactory {
	t.Helper()
	box, err := reasoning.NewToolbox(reasoning.Tool{
		Name: reasoning.ToolWatchLive, Desc: "看当前直播",
		Run: func(context.Context, string) (string, error) {
			*calls++
			return "画面：桌上有面包\n主播刚说：好饿\n最近弹幕：甲：吃了吗", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return func(memory.Scope) (*reasoning.Agent, error) {
		return reasoning.New(llm, box, reasoning.Options{MaxSteps: 3, Timeout: 2 * time.Second}, zap.NewNop())
	}
}

// TestRunOnceReactWatchLiveStripsPassiveInjection 注册 watch_live 时，
// 画面/声音/弹幕不再被动注入，改为提示模型主动调用工具读取。
func TestRunOnceReactWatchLiveStripsPassiveInjection(t *testing.T) {
	f := newFixture(t)
	audio := &fakeAudio{text: "主播：今天吃鱼"}
	chat := &fakeChat{rows: []event.Event{{FromID: "1", FromName: "甲", Text: "弹幕正文"}}}
	llm := &sequenceLLM{replies: []string{
		`{"thought":"先看直播","action":"watch_live","args":""}`,
		`{"thought":"够了","action":"final","final":"在吃面包呢"}`,
		"在吃面包呢",
	}}
	toolCalls := 0
	factory := reactWithWatchLive(t, llm, &toolCalls)
	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, factory, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Audio: audio, Chat: chat, Senses: &fakeSenses{},
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if !got.React || !got.Sent || got.Text != "在吃面包呢" {
		t.Fatalf("结果不符：%+v", got)
	}
	if toolCalls != 1 {
		t.Errorf("模型应主动调用一次 watch_live：%d", toolCalls)
	}
	for _, banned := range []string{"<visual>", "<audio>", "<live_chat>", f.vision.text, audio.text, "弹幕正文"} {
		if strings.Contains(llm.firstUser, banned) {
			t.Errorf("主动看直播时不应被动注入 %q：\n%s", banned, llm.firstUser)
		}
	}
	if !strings.Contains(llm.firstUser, "先调用 watch_live") {
		t.Errorf("Question 未提示主动看直播：\n%s", llm.firstUser)
	}
	if !strings.Contains(llm.firstSystem, "①字面") {
		t.Error("主动弹幕 ReAct 应开启三层 thought")
	}
}

// TestRunOnceReactWithoutWatchLiveKeepsPassiveInjection 没注册 watch_live 时保持旧行为：
// 画面/声音/弹幕照常被动注入，也不追加调用提示。
func TestRunOnceReactWithoutWatchLiveKeepsPassiveInjection(t *testing.T) {
	f := newFixture(t)
	audio := &fakeAudio{text: "主播：今天吃鱼"}
	chat := &fakeChat{rows: []event.Event{{FromID: "1", FromName: "甲", Text: "弹幕正文"}}}
	llm := &sequenceLLM{replies: []string{
		`{"thought":"够了","action":"final","final":"在吃面包呢"}`,
		"在吃面包呢",
	}}
	factory, _ := reactFixture(t, llm)
	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, factory, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Audio: audio, Chat: chat, Senses: &fakeSenses{},
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	for _, want := range []string{"<visual>", f.vision.text, "<audio>", audio.text, "<live_chat>", "弹幕正文"} {
		if !strings.Contains(llm.firstUser, want) {
			t.Errorf("旧行为应被动注入 %q：\n%s", want, llm.firstUser)
		}
	}
	if strings.Contains(llm.firstUser, "先调用 watch_live") {
		t.Errorf("未注册 watch_live 不应出现调用提示：\n%s", llm.firstUser)
	}
}

// TestBuildInputSetsAudioAndChat buildInput 在 Builder/最小召回两条路都要补上声音与弹幕。
func TestBuildInputSetsAudioAndChat(t *testing.T) {
	f := newFixture(t)
	builder := &fakeBuilder{input: snapshotBuilderInput()}
	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, nil, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220", Builder: builder,
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	ev := runner.syntheticEvent(observation{ID: "o1", CapturedAt: *f.clock})
	in := runner.buildInput(context.Background(), ev, "画面", "声音", []string{"甲：一", "乙：二"})
	if in.Audio != "声音" || in.LiveChat != "甲：一\n乙：二" {
		t.Errorf("Builder 路径未补四感：audio=%q chat=%q", in.Audio, in.LiveChat)
	}
	if in.Visual != "画面" {
		t.Errorf("画面应来自 Builder：%q", in.Visual)
	}

	runner, err = New(f.snap, f.vision, f.engine, testPack(), nil, nil, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	in = runner.buildInput(context.Background(), ev, "画面", "声音", []string{"甲：一"})
	if in.Audio != "声音" || in.LiveChat != "甲：一" || in.Visual != "画面" {
		t.Errorf("最小召回路径未补四感：%+v", in)
	}
	if in.Persona.Meta.Name == "" {
		t.Error("最小召回应带人格包")
	}
}
