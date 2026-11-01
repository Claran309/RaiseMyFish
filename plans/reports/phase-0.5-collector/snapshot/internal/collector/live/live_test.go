package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

func TestNormalizeStableIdentityAndSafety(t *testing.T) {
	frame := wireFrame{Protocol: 1, Type: "event", RoomID: "99", MsgID: "platform-123", FromID: "7", FromName: "观众", Text: "你好", At: "2026-11-01T08:00:00Z"}
	ev, keep, err := normalize(frame, "99", "8")
	if err != nil || !keep || ev.ID != "live:99:platform-123" || ev.Status != event.StatusPending || ev.Private || ev.Dir != event.DirIn {
		t.Fatal("归一化错误", err)
	}
	self := frame
	self.FromID = "8"
	if _, keep, err := normalize(self, "99", "8"); err != nil || keep {
		t.Fatal("自身消息未过滤")
	}
	for _, mutate := range []func(*wireFrame){func(f *wireFrame) { f.MsgID = "" }, func(f *wireFrame) { f.MsgID = "bad:id" }, func(f *wireFrame) { f.RoomID = "other" }, func(f *wireFrame) { f.FromID = "" }, func(f *wireFrame) { f.At = "bad" }, func(f *wireFrame) { f.Text = "" }, func(f *wireFrame) { f.Protocol = 2 }} {
		bad := frame
		mutate(&bad)
		if _, _, err := normalize(bad, "99", "8"); err == nil {
			t.Fatal("坏事件未拒绝")
		}
	}
	frame.Text = strings.Repeat("鱼", 4001)
	ev, keep, err = normalize(frame, "99", "8")
	if err != nil || !keep || len([]rune(ev.Text)) != 4000 {
		t.Fatal("字符截断错误", err)
	}
	var meta struct {
		Truncated bool `json:"truncated"`
		Original  int  `json:"original_runes"`
	}
	if err := json.Unmarshal(ev.Meta, &meta); err != nil || !meta.Truncated || meta.Original != 4001 {
		t.Fatal("缺截断依据", err)
	}
}

func helperOptions() Options {
	o := DefaultOptions()
	o.RoomID = "1"
	o.SelfID = "8"
	o.Cookie = "SESSDATA=credential-canary"
	o.ScriptPath = "unused-fixture.mjs"
	o.BackoffMin = time.Millisecond
	o.BackoffMax = 4 * time.Millisecond
	o.MaxFailures = 4
	return o
}
func newHelper(t *testing.T, mode string) *Collector {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(helperOptions(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	c.command = func(ctx context.Context) *exec.Cmd {
		// #nosec G204 -- 仅执行当前测试二进制的固定helper，不使用消息/凭据作为命令参数。
		cmd := exec.CommandContext(ctx, executable, "-test.run=TestLiveChildHelper")
		cmd.Env = append(os.Environ(), "FISH_TEST_LIVE_CHILD="+mode)
		return cmd
	}
	return c
}

func TestLiveChildHelper(t *testing.T) {
	mode := os.Getenv("FISH_TEST_LIVE_CHILD")
	if mode == "" {
		return
	}
	if mode == "silent" {
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	if mode == "auth" {
		fmt.Println(`{"protocol":1,"type":"error","kind":"auth","fatal":true}`)
		os.Exit(10)
	}
	if mode == "rate" {
		fmt.Println(`{"protocol":1,"type":"error","kind":"rate_limit","fatal":false,"retryable":true}`)
		os.Exit(11)
	}
	fmt.Println(`{"protocol":1,"type":"ready","room_id":"99","self_id":"8"}`)
	if mode == "long" {
		data, _ := json.Marshal(wireFrame{Protocol: 1, Type: "event", RoomID: "99", MsgID: "long", FromID: "7", FromName: "观众", Text: strings.Repeat("a", 600000), At: "2026-11-01T08:00:00Z"})
		fmt.Println(string(data))
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	if mode == "bad" {
		fmt.Println("private-cookie-canary not json")
	}
	fmt.Println(`{"protocol":1,"type":"event","room_id":"99","msg_id":"self","from_id":"8","from_name":"鱼","text":"自己","at":"2026-11-01T08:00:00Z"}`)
	for range 2 {
		fmt.Println(`{"protocol":1,"type":"event","room_id":"99","msg_id":"same","from_id":"7","from_name":"观众","text":"普通观众消息","at":"2026-11-01T08:00:00Z"}`)
	}
	if mode == "hold" {
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}

func TestProcessMessagesHealthAndCancellation(t *testing.T) {
	c := newHelper(t, "hold")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan event.Event, 2)
	errc := make(chan error, 4)
	finished := make(chan error, 1)
	go func() { finished <- c.Start(ctx, out, errc) }()
	for range 2 {
		select {
		case ev := <-out:
			if ev.ID != "live:99:same" {
				t.Fatal("自身消息或错误键")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("没有子进程消息")
		}
	}
	h := c.Health()
	if !h.OK || h.LastEventAt.IsZero() {
		t.Fatal("健康状态缺失")
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消未终止并等待子进程")
	}
	if c.Health().OK {
		t.Fatal("退出仍显示健康")
	}
}

func TestReconnectBackoffBoundAndAuthPause(t *testing.T) {
	c := newHelper(t, "close")
	var delays []time.Duration
	c.pause = func(ctx context.Context, d time.Duration) error { delays = append(delays, d); return ctx.Err() }
	out := make(chan event.Event, 16)
	errc := make(chan error, 32)
	err := c.Start(context.Background(), out, errc)
	if err == nil || len(delays) != 3 || delays[0] != time.Millisecond || delays[1] != 2*time.Millisecond || delays[2] != 4*time.Millisecond || c.Health().Reconnects != 3 {
		t.Fatal("退避或上限错误", delays, err)
	}
	c = newHelper(t, "auth")
	err = c.Start(context.Background(), out, errc)
	var ce *Error
	if !errors.As(err, &ce) || ce.Kind != KindAuth || c.Health().Reconnects != 0 {
		t.Fatal("鉴权失败仍重连", err)
	}
}

func TestMalformedOutputDoesNotLeakAndBlockedOutputCancels(t *testing.T) {
	c := newHelper(t, "bad")
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan event.Event, 4)
	errc := make(chan error, 8)
	finished := make(chan error, 1)
	go func() { finished <- c.Start(ctx, out, errc) }()
	select {
	case err := <-errc:
		if strings.Contains(err.Error(), "canary") {
			t.Fatal("解析错误泄漏子进程正文")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未报告解析失败")
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	c = newHelper(t, "hold")
	ctx, cancel = context.WithCancel(context.Background())
	blocked := make(chan event.Event, 1)
	finished = make(chan error, 1)
	go func() { finished <- c.Start(ctx, blocked, errc) }()
	// 等第一条实际填满有界输出，再取消；不靠任意sleep假设子进程已经启动。
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for c.Health().LastEventAt.IsZero() {
		select {
		case err := <-finished:
			cancel()
			t.Fatal("子进程在背压前退出", err)
		case <-deadline.C:
			cancel()
			t.Fatal("未进入有界输出状态")
		case <-tick.C:
		}
	}
	if len(blocked) != 1 {
		cancel()
		t.Fatal("输出未填满")
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("背压时取消未退出")
	}
}

func TestSubprocessEnvironmentOmitsOtherSecrets(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "database-secret-canary")
	t.Setenv("FISH_LLM_KEY", "model-secret-canary")
	t.Setenv("NODE_OPTIONS", "--require malicious")
	c, err := New(helperOptions(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	cmd := c.buildCommand(context.Background())
	joined := strings.Join(cmd.Env, "\n")
	if strings.Contains(joined, "database-secret-canary") || strings.Contains(joined, "model-secret-canary") || strings.Contains(joined, "NODE_OPTIONS") {
		t.Fatal("继承了非采集秘密或Node注入参数")
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "credential-canary") {
		t.Fatal("凭据出现在命令行")
	}
}

func TestHealthConcurrentReads(t *testing.T) {
	c := newHelper(t, "hold")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan event.Event, 4)
	errc := make(chan error, 4)
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx, out, errc) }()
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			for range 500 {
				_ = c.Health()
			}
		})
	}
	wg.Wait()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNodeOutputLimitReachesRuneTruncation(t *testing.T) {
	c := newHelper(t, "long")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := make(chan event.Event, 1)
	errc := make(chan error, 16)
	finished := make(chan error, 1)
	go func() { finished <- c.Start(ctx, out, errc) }()
	select {
	case ev := <-out:
		if len([]rune(ev.Text)) != 4000 {
			t.Fatal("没有字符截断")
		}
		cancel()
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	case err := <-finished:
		t.Fatal("Node合法大小输出在Go扫描层丢失", err)
	case <-ctx.Done():
		if err := <-finished; err != nil {
			t.Error(err)
		}
		t.Fatal("未收到Node合法大小输出")
	}
}

func TestRetryableChildClassificationIsPreserved(t *testing.T) {
	c := newHelper(t, "rate")
	c.options.MaxFailures = 1
	err := c.Start(context.Background(), make(chan event.Event, 1), make(chan error, 4))
	var final *Error
	if !errors.As(err, &final) || final.Kind != KindExhausted {
		t.Fatal("缺少有界失败", err)
	}
	var cause *Error
	if !errors.As(errors.Unwrap(err), &cause) || cause.Kind != KindRate {
		t.Fatal("可重试限流被误归网络中断", err)
	}
}
