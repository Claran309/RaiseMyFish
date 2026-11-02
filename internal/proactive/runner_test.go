package proactive

import (
	"context"
	"errors"
	"image/png"
	"os"
	"testing"
	"time"

	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

type fakeSnapshotter struct {
	paths []string
	calls int
	err   error
}

func (f *fakeSnapshotter) Snapshot(context.Context) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.calls >= len(f.paths) {
		return "", errors.New("没有更多帧")
	}
	path := f.paths[f.calls]
	f.calls++
	return path, nil
}

type fakeDescriber struct {
	texts  []string
	calls  int
	frames int
	prompt string
	err    error
}

func (f *fakeDescriber) Describe(_ context.Context, frames []string, prompt string) (string, error) {
	f.calls++
	f.frames, f.prompt = len(frames), prompt
	if f.err != nil {
		return "", f.err
	}
	if f.calls > len(f.texts) {
		return "", errors.New("未预期的读帧调用")
	}
	return f.texts[f.calls-1], nil
}

type fakeProactiveSender struct {
	texts  []string
	result sender.Result
	err    error
}

func (f *fakeProactiveSender) Send(_ context.Context, text string) (sender.Result, error) {
	f.texts = append(f.texts, text)
	return f.result, f.err
}

func frameFile(t *testing.T, value uint8) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "frame-*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := png.Encode(file, solid(10, 10, value)); err != nil {
		t.Fatal(err)
	}
	return file.Name()
}

func testRunner(t *testing.T, snapshot Snapshotter, describer Describer, snd Sender, maxPerHour int, minChange float64) *Runner {
	t.Helper()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	r, err := New(snapshot, describer, snd, Options{
		MaxPerHour:       maxPerHour,
		MinChangePercent: minChange,
		Logger:           zap.NewNop(),
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunnerSendsOnFirstFrame(t *testing.T) {
	path := frameFile(t, 0)
	snapshot := &fakeSnapshotter{paths: []string{path}}
	describer := &fakeDescriber{texts: []string{"鱼鱼可爱"}}
	snd := &fakeProactiveSender{result: sender.Result{OK: true, Verified: true}}
	runner := testRunner(t, snapshot, describer, snd, 6, 5)

	result, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Sent || result.ChangePercent != 100 {
		t.Fatalf("首帧应视为全画面变化并发送：%+v", result)
	}
	if len(snd.texts) != 1 || snd.texts[0] != "鱼鱼可爱" {
		t.Fatalf("发送内容不符：%v", snd.texts)
	}
	if describer.frames != 1 || describer.prompt == "" {
		t.Fatalf("读帧参数不符：frames=%d prompt=%q", describer.frames, describer.prompt)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("截帧临时文件未清理")
	}
}

func TestRunnerSkipsWithinSendGap(t *testing.T) {
	snapshot := &fakeSnapshotter{paths: []string{frameFile(t, 0), frameFile(t, 255), frameFile(t, 128)}}
	describer := &fakeDescriber{texts: []string{"第一条", "第二条"}}
	snd := &fakeProactiveSender{result: sender.Result{OK: true, Verified: true}}
	runner := testRunner(t, snapshot, describer, snd, 6, 5)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	runner.now = func() time.Time { return now }

	if result, err := runner.RunOnce(context.Background()); err != nil || !result.Sent {
		t.Fatalf("首帧应发送：%+v err=%v", result, err)
	}
	now = now.Add(10 * time.Second)
	result, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent || result.Reason != ReasonGap {
		t.Fatalf("30秒内第二次发送应跳过且 reason=gap：%+v", result)
	}
	if snapshot.calls != 1 || describer.calls != 1 || len(snd.texts) != 1 {
		t.Fatalf("间隔内不应截帧/读帧/发送：snapshot=%d describer=%d sent=%v", snapshot.calls, describer.calls, snd.texts)
	}
	now = now.Add(20 * time.Second) // 距上次发送恰好30秒
	result, err = runner.RunOnce(context.Background())
	if err != nil || !result.Sent {
		t.Fatalf("满30秒应恢复发送：%+v err=%v", result, err)
	}
	if len(snd.texts) != 2 || snd.texts[1] != "第二条" {
		t.Fatalf("恢复后发送内容不符：%v", snd.texts)
	}
}

func TestRunnerSkipsUnchangedFrame(t *testing.T) {
	snapshot := &fakeSnapshotter{paths: []string{frameFile(t, 0), frameFile(t, 0)}}
	describer := &fakeDescriber{texts: []string{"第一条"}}
	snd := &fakeProactiveSender{result: sender.Result{OK: true, Verified: true}}
	runner := testRunner(t, snapshot, describer, snd, 6, 5)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	runner.now = func() time.Time { return now }

	if result, err := runner.RunOnce(context.Background()); err != nil || !result.Sent {
		t.Fatalf("首帧应发送：%+v err=%v", result, err)
	}
	now = now.Add(30 * time.Second)
	result, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent || result.Reason != ReasonNoChange {
		t.Fatalf("画面未变化应跳过：%+v", result)
	}
	if describer.calls != 1 || len(snd.texts) != 1 {
		t.Fatalf("变化不足时不应读帧/发送：calls=%d sent=%v", describer.calls, snd.texts)
	}
}

func TestRunnerSkipsSimilarText(t *testing.T) {
	snapshot := &fakeSnapshotter{paths: []string{frameFile(t, 0), frameFile(t, 255)}}
	describer := &fakeDescriber{texts: []string{"鱼鱼好可爱", "鱼鱼好可爱！"}}
	snd := &fakeProactiveSender{result: sender.Result{OK: true, Verified: true}}
	runner := testRunner(t, snapshot, describer, snd, 6, 5)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	runner.now = func() time.Time { return now }

	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Second)
	result, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent || result.Reason != ReasonSimilar {
		t.Fatalf("相似弹幕应跳过：%+v", result)
	}
	if len(snd.texts) != 1 {
		t.Fatalf("相似弹幕被重复发送：%v", snd.texts)
	}
}

func TestRunnerSkipsOverLimit(t *testing.T) {
	snapshot := &fakeSnapshotter{paths: []string{frameFile(t, 0), frameFile(t, 255)}}
	describer := &fakeDescriber{texts: []string{"第一条"}}
	snd := &fakeProactiveSender{result: sender.Result{OK: true, Verified: true}}
	runner := testRunner(t, snapshot, describer, snd, 1, 5)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	runner.now = func() time.Time { return now }

	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Second)
	result, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Sent || result.Reason != ReasonLimited {
		t.Fatalf("超过每小时上限应跳过：%+v", result)
	}
	if len(snd.texts) != 1 || snapshot.calls != 1 {
		t.Fatalf("超限后不应再截帧/发送：snapshot=%d sent=%v", snapshot.calls, snd.texts)
	}
}

func TestRunnerDescriberFailureDoesNotSend(t *testing.T) {
	path := frameFile(t, 0)
	snapshot := &fakeSnapshotter{paths: []string{path}}
	describer := &fakeDescriber{err: errors.New("视觉模型不可用")}
	snd := &fakeProactiveSender{result: sender.Result{OK: true}}
	runner := testRunner(t, snapshot, describer, snd, 6, 5)

	if _, err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("读帧失败应返回错误")
	}
	if len(snd.texts) != 0 {
		t.Fatal("读帧失败后仍发送")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("失败时截帧临时文件未清理")
	}
}

func TestNewRunnerRejectsInvalidOptions(t *testing.T) {
	snapshot := &fakeSnapshotter{}
	describer := &fakeDescriber{}
	snd := &fakeProactiveSender{}
	if _, err := New(nil, describer, snd, Options{MaxPerHour: 6}); err == nil {
		t.Error("缺少截帧器应报错")
	}
	if _, err := New(snapshot, nil, snd, Options{MaxPerHour: 6}); err == nil {
		t.Error("缺少读帧器应报错")
	}
	if _, err := New(snapshot, describer, nil, Options{MaxPerHour: 6}); err == nil {
		t.Error("缺少发送器应报错")
	}
	if _, err := New(snapshot, describer, snd, Options{MaxPerHour: 0}); err == nil {
		t.Error("max_per_hour 非法应报错")
	}
	if _, err := New(snapshot, describer, snd, Options{MaxPerHour: 6, MinChangePercent: 101}); err == nil {
		t.Error("min_change_percent 非法应报错")
	}
	if _, err := New(snapshot, describer, snd, Options{MaxPerHour: 6, SendGap: -time.Second}); err == nil {
		t.Error("负的 send_gap 应报错")
	}
	if _, err := New(snapshot, describer, snd, Options{MaxPerHour: 6, SendGap: time.Hour + time.Second}); err == nil {
		t.Error("超上限的 send_gap 应报错")
	}
	runner, err := New(snapshot, describer, snd, Options{MaxPerHour: 6})
	if err != nil {
		t.Fatal(err)
	}
	if runner.limiter.gap != DefaultSendGap {
		t.Fatalf("未配置 send_gap 时应使用默认30秒：%v", runner.limiter.gap)
	}
}
