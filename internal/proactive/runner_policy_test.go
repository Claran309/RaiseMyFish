package proactive

// 本文件测 N22 的端到端策略：四态、观察 ID/时间、5 分钟不变、追问让路、
// 语义去重，以及「失败尝试不算说过」。

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
)

func newFixtureWithBuilder(t *testing.T, builder ContextBuilder) *fixture {
	t.Helper()
	f := newFixture(t)
	opts := Options{
		SendGap: 30 * time.Second, RoomID: "30308220", Builder: builder,
		Now: func() time.Time { return *f.clock },
	}
	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, nil, f.snd, opts)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	f.runner = runner
	return f
}

func conversationBuilder(base time.Time, messages ...memory.Message) *fakeBuilder {
	return &fakeBuilder{input: brain.ContextInput{
		Memory: memory.Context{
			Scope:  memory.Scope{Channel: event.ChLive, Session: "30308220"},
			Recent: messages,
		},
	}}
}

// TestObservationIDAndTimeBoundToTrigger 观察 ID 同时用于事件/Task/Result，
// 时间取截帧时刻。
func TestObservationIDAndTimeBoundToTrigger(t *testing.T) {
	f := newFixture(t)
	got, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if got.ObservationID == "" || !got.ObservedAt.Equal(*f.clock) {
		t.Fatalf("结果未绑定观察：%+v", got)
	}
	if got.ObservationID != f.engine.gotTask.EventID || got.ObservationID != f.engine.gotInput.Event.ID {
		t.Errorf("观察 ID 未贯穿事件/Task：%q / %q / %q",
			got.ObservationID, f.engine.gotTask.EventID, f.engine.gotInput.Event.ID)
	}
	if !got.ObservedAt.Equal(f.engine.gotInput.Event.At) {
		t.Errorf("事件时间与观察时间不一致：%v / %v", f.engine.gotInput.Event.At, got.ObservedAt)
	}
	if got.Action != ActionReact {
		t.Errorf("首次看到新画面应为 react：%+v", got)
	}
}

// TestRunOnceStaysSilentWhenSceneUnchangedForFiveMinutes 画面连续 5 分钟不变：
// 第一轮可以说，之后保持安静，不再截帧后的模型调用与发送。
func TestRunOnceStaysSilentWhenSceneUnchangedForFiveMinutes(t *testing.T) {
	f := newFixture(t)
	first, err := f.runner.RunOnce(context.Background())
	if err != nil || !first.Sent {
		t.Fatalf("首轮应发送：%+v / %v", first, err)
	}
	firstID := first.ObservationID
	*f.clock = f.clock.Add(5 * time.Minute)

	second, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	if second.Sent || second.Action != ActionStaySilent || second.Reason != ReasonNoNewFact {
		t.Fatalf("画面不变应保持安静：%+v", second)
	}
	if second.ObservationID == "" || second.ObservationID == firstID {
		t.Errorf("每轮仍应有独立观察 ID：first=%q second=%q", firstID, second.ObservationID)
	}
	if !second.ObservedAt.Equal(*f.clock) {
		t.Errorf("观察时间应为本轮截帧时间：%v", second.ObservedAt)
	}
	if f.engine.calls != 1 || f.snd.calls != 1 {
		t.Errorf("画面不变不应再生成/发送：brain=%d send=%d", f.engine.calls, f.snd.calls)
	}
	if f.snap.calls != 2 || f.vision.calls != 2 {
		t.Errorf("仍应截帧识图以判断新颖性：snap=%d vision=%d", f.snap.calls, f.vision.calls)
	}
}

// TestRunOnceYieldsThenResumesAfterAnswer 用户追问未回答时让路；已有回复后，
// 新画面可以恢复主动发言。
func TestRunOnceYieldsThenResumesAfterAnswer(t *testing.T) {
	base := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	question := testMessage("live:in:1", event.DirIn, "刚才那个梗是什么意思？", base.Add(-30*time.Second))
	builder := conversationBuilder(base, question)
	f := newFixtureWithBuilder(t, builder)

	got, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if got.Sent || got.Action != ActionStaySilent || got.Reason != ReasonYieldFollowUp {
		t.Fatalf("未回答的追问应让路：%+v", got)
	}
	if f.engine.calls != 0 || f.snd.calls != 0 {
		t.Errorf("让路时不应生成/发送：brain=%d send=%d", f.engine.calls, f.snd.calls)
	}
	if got.ObservationID == "" {
		t.Error("让路也应绑定观察 ID")
	}

	// 被动回复已经回答该问题；换一帧后应恢复。
	answer := testMessage("live:out:1", event.DirOut, "就是那个笑点", base.Add(-10*time.Second))
	answer.ReplyTo = question.EventID
	builder.input.Memory.Recent = append(builder.input.Memory.Recent, answer)
	*f.clock = f.clock.Add(time.Minute)
	f.vision.text = "主播端起水杯喝了一口，背景弹幕还在滚动。"

	resumed, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("恢复轮失败：%v", err)
	}
	if !resumed.Sent || resumed.Action == ActionStaySilent {
		t.Fatalf("回答后应恢复主动发言：%+v", resumed)
	}
}

// TestRunOnceYieldsToCorrection 用户纠正时让路。
func TestRunOnceYieldsToCorrection(t *testing.T) {
	base := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	correction := testMessage("live:in:2", event.DirIn, "你刚才说错了，不是那样", base.Add(-20*time.Second))
	f := newFixtureWithBuilder(t, conversationBuilder(base, correction))

	got, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if got.Sent || got.Action != ActionStaySilent || got.Reason != ReasonYieldCorrection {
		t.Fatalf("纠正应让路：%+v", got)
	}
	if f.engine.calls != 0 || f.snd.calls != 0 {
		t.Errorf("让路时不应生成/发送：brain=%d send=%d", f.engine.calls, f.snd.calls)
	}
}

// TestRunOnceAllowsConversationChangeWithSameScene 画面没变但用户说了新内容：
// 允许发言，并标记为 comment（不是 react）。
func TestRunOnceAllowsConversationChangeWithSameScene(t *testing.T) {
	base := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	builder := conversationBuilder(base)
	f := newFixtureWithBuilder(t, builder)

	first, err := f.runner.RunOnce(context.Background())
	if err != nil || !first.Sent {
		t.Fatalf("首轮应发送：%+v / %v", first, err)
	}
	*f.clock = f.clock.Add(time.Minute)
	builder.input.Memory.Recent = []memory.Message{
		testMessage("live:in:3", event.DirIn, "今天这主播状态不错", base.Add(-10*time.Second)),
	}
	f.engine.reply = brain.Reply{Text: "确实挺精神"}

	second, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	if !second.Sent || second.Action != ActionComment {
		t.Fatalf("画面不变但对话有变化应允许 comment：%+v", second)
	}
	if f.engine.calls != 2 || f.snd.calls != 2 {
		t.Errorf("应重新生成并发送：brain=%d send=%d", f.engine.calls, f.snd.calls)
	}
}

// TestRunOnceSemanticViewpointDuplicate 画面描述换了措辞但仍是同一场景：
// 不进入生成，直接 stay_silent。
func TestRunOnceSemanticViewpointDuplicate(t *testing.T) {
	f := newFixture(t)
	f.vision.text = "主播坐在桌前打游戏，右下角弹幕不停滚动"
	first, err := f.runner.RunOnce(context.Background())
	if err != nil || !first.Sent {
		t.Fatalf("首轮应发送：%+v / %v", first, err)
	}
	*f.clock = f.clock.Add(time.Minute)
	f.vision.text = "主播坐在桌前玩着游戏，右下角弹幕还在滚动"
	f.engine.reply = brain.Reply{Text: "换个说法也不行"}

	second, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	if second.Sent || second.Action != ActionStaySilent || second.Reason != ReasonSemanticView {
		t.Fatalf("同一观点的另一种说法应保持安静：%+v", second)
	}
	if f.engine.calls != 1 || f.snd.calls != 1 {
		t.Errorf("语义观点重复不应重新生成/发送：brain=%d send=%d", f.engine.calls, f.snd.calls)
	}
}

// TestRunOnceSemanticTextDuplicate 新画面生成了语义近似、字面不完全相同的话：
// 不重发。
func TestRunOnceSemanticTextDuplicate(t *testing.T) {
	f := newFixture(t)
	f.engine.reply = brain.Reply{Text: "这界面有点东西"}
	first, err := f.runner.RunOnce(context.Background())
	if err != nil || !first.Sent {
		t.Fatalf("首轮应发送：%+v / %v", first, err)
	}
	*f.clock = f.clock.Add(time.Minute)
	f.vision.text = "窗外下着小雨，一只黑猫趴在窗台上看风景。"
	f.engine.reply = brain.Reply{Text: "这界面有点内容"}

	second, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	if second.Sent || second.Action != ActionStaySilent || second.Reason != ReasonSimilar {
		t.Fatalf("语义近似内容应被去重：%+v", second)
	}
	if f.engine.calls != 2 || f.snd.calls != 1 {
		t.Errorf("应生成但不重发：brain=%d send=%d", f.engine.calls, f.snd.calls)
	}
}

// TestFailedAttemptDoesNotCountAsSpoken 发送失败/未接受只进审计，
// 不写「已经说过」；换一帧后同一句仍可尝试。
func TestFailedAttemptDoesNotCountAsSpoken(t *testing.T) {
	cases := []struct {
		name    string
		fail    func(*fakeSender)
		outcome string
	}{
		{
			name:    "发送报错",
			fail:    func(s *fakeSender) { s.err = errors.New("send down") },
			outcome: "send_error",
		},
		{
			name: "平台未接受",
			fail: func(s *fakeSender) {
				s.result = sender.Result{OK: false, Error: "content"}
			},
			outcome: "not_accepted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.fail(f.snd)

			first, err := f.runner.RunOnce(context.Background())
			if tc.outcome == "send_error" {
				if err == nil {
					t.Fatal("发送报错应返回错误")
				}
			} else if err != nil {
				t.Fatalf("平台未接受不应返回错误：%v", err)
			}
			if first.Sent {
				t.Fatalf("失败尝试不应标记已发送：%+v", first)
			}
			if len(f.runner.recent.texts) != 0 {
				t.Fatalf("失败尝试不应进入已说记录：%+v", f.runner.recent.texts)
			}
			if len(f.runner.policy.attempts) != 1 || f.runner.policy.attempts[0].Outcome != tc.outcome {
				t.Fatalf("失败尝试应单独审计：%+v", f.runner.policy.attempts)
			}

			// 换一帧、发送恢复正常：同一句话可以重新尝试。
			*f.clock = f.clock.Add(time.Minute)
			f.vision.text = "窗外下着小雨，一只黑猫趴在窗台上看风景。"
			f.snd.err = nil
			f.snd.result = sender.Result{OK: true, Verified: true}

			second, err := f.runner.RunOnce(context.Background())
			if err != nil {
				t.Fatalf("重试轮失败：%v", err)
			}
			if !second.Sent {
				t.Fatalf("失败尝试不应阻断重试：%+v", second)
			}
			if len(f.runner.recent.texts) != 1 {
				t.Fatalf("确认发送后才写入已说记录：%+v", f.runner.recent.texts)
			}
		})
	}
}

// TestBrainFailureDoesNotCommitObservation 生成失败不算「已经看过」，
// 下一轮同一画面仍可重试。
func TestBrainFailureDoesNotCommitObservation(t *testing.T) {
	f := newFixture(t)
	f.engine.err = errors.New("brain down")
	if _, err := f.runner.RunOnce(context.Background()); err == nil {
		t.Fatal("Brain 失败应返回错误")
	}
	if _, seen := f.runner.policy.lastObservation(); seen {
		t.Fatal("生成失败不应提交观察")
	}

	f.engine.err = nil
	*f.clock = f.clock.Add(time.Minute)
	got, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("重试轮失败：%v", err)
	}
	if !got.Sent {
		t.Fatalf("同一画面在生成失败后应可重试：%+v", got)
	}
}

// TestRunOnceActionAsk 生成问句时动作明确为 ask。
func TestRunOnceActionAsk(t *testing.T) {
	f := newFixture(t)
	f.engine.reply = brain.Reply{Text: "大家吃了吗？"}

	got, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if !got.Sent || got.Action != ActionAsk {
		t.Fatalf("问句动作应为 ask：%+v", got)
	}
}
