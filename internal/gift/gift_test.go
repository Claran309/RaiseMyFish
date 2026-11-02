package gift

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	helperGift = "31036"
	helperRoom = "99"
)

// TestGiftHelperProcess 充当子进程：只写 stdout 协议帧后立即退出，避免测试框架追加输出。
func TestGiftHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GIFT_HELPER") != "1" {
		return
	}
	mode := os.Getenv("GIFT_HELPER_MODE")
	giftID := os.Getenv("GIFT_HELPER_GIFT")
	num := os.Getenv("GIFT_HELPER_NUM")
	sent := fmt.Sprintf(`{"protocol":1,"type":"sent","gift_id":%q,"num":%s}`+"\n", giftID, num)
	switch mode {
	case "sent":
		fmt.Print(sent)
	case "sent-exit-1":
		fmt.Print(sent)
		os.Exit(1)
	case "retryable":
		fmt.Print(`{"protocol":1,"type":"error","kind":"retryable","message":"平台暂时不可用"}` + "\n")
		os.Exit(1)
	case "fatal":
		fmt.Print(`{"protocol":1,"type":"error","kind":"fatal","message":"礼物发送被拒绝（code=-400）"}` + "\n")
		os.Exit(2)
	case "bad-kind":
		fmt.Print(`{"protocol":1,"type":"error","kind":"unknown","message":"x"}` + "\n")
		os.Exit(2)
	case "bad-message":
		fmt.Print(`{"protocol":1,"type":"error","kind":"fatal","message":"line\nbreak"}` + "\n")
		os.Exit(2)
	case "empty":
	case "partial":
		fmt.Print(`{"protocol":1,`)
	case "protocol-2":
		fmt.Print(`{"protocol":2,"type":"sent","gift_id":"31036","num":1}` + "\n")
	case "unknown-field":
		fmt.Print(`{"protocol":1,"type":"sent","gift_id":"31036","num":1,"extra":true}` + "\n")
	case "two-lines":
		fmt.Print(sent + sent)
	case "mismatch":
		fmt.Print(`{"protocol":1,"type":"sent","gift_id":"999","num":1}` + "\n")
	case "oversize":
		fmt.Print(strings.Repeat("x", maxOutput+1))
	case "stderr":
		fmt.Fprint(os.Stderr, "credential-private-canary")
		fmt.Print(sent)
	case "hang":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func newSender(t *testing.T, o Options) *NodeSender {
	t.Helper()
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func helperCommand(mode string, spawns *int32) func(context.Context, string, string, int) *exec.Cmd {
	return func(ctx context.Context, room, giftID string, num int) *exec.Cmd {
		if spawns != nil {
			atomic.AddInt32(spawns, 1)
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestGiftHelperProcess")
		cmd.Env = append(os.Environ(),
			"GO_WANT_GIFT_HELPER=1",
			"GIFT_HELPER_MODE="+mode,
			"GIFT_HELPER_GIFT="+giftID,
			"GIFT_HELPER_NUM="+strconv.Itoa(num),
		)
		cmd.Stderr = io.Discard
		return cmd
	}
}

func realOptions() Options {
	o := DefaultOptions()
	o.DryRun = false
	o.Cookie = "SESSDATA=fixture; bili_jct=fixture; buvid3=fixture"
	return o
}

func TestNewValidation(t *testing.T) {
	if _, err := New(DefaultOptions()); err != nil {
		t.Fatalf("默认dry-run配置应有效：%v", err)
	}
	if _, err := New(realOptions()); err != nil {
		t.Fatalf("带凭据的真送配置应有效：%v", err)
	}
	file := realOptions()
	file.Cookie, file.CookieFile = "", "cookie.txt"
	if _, err := New(file); err != nil {
		t.Fatalf("cookie文件配置应有效：%v", err)
	}
	for name, mutate := range map[string]func(*Options){
		"空node":   func(o *Options) { o.NodePath = " " },
		"空script": func(o *Options) { o.ScriptPath = "" },
		"路径控制字符":  func(o *Options) { o.ScriptPath = "a\nb" },
		"超时过短":    func(o *Options) { o.Timeout = time.Millisecond },
		"超时过长":    func(o *Options) { o.Timeout = time.Hour },
		"限额为零":    func(o *Options) { o.DailyLimit = 0 },
		"限额过大":    func(o *Options) { o.DailyLimit = 101 },
		"真送缺凭据":   func(o *Options) { o.DryRun = false; o.Cookie, o.CookieFile = "", "" },
		"凭据含控制字符": func(o *Options) { o.Cookie = "SESSDATA=x\r\n" },
	} {
		t.Run(name, func(t *testing.T) {
			o := DefaultOptions()
			mutate(&o)
			_, err := New(o)
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != KindFatal {
				t.Fatalf("无效配置未被拒绝：%v", err)
			}
		})
	}
}

func TestDryRunClearsCredentialsAndBuildsSafeCommand(t *testing.T) {
	o := DefaultOptions()
	o.Cookie = "SESSDATA=leak; bili_jct=x; buvid3=y"
	o.CookieFile = "private-cookie.txt"
	s := newSender(t, o)
	cmd := s.buildCommand(context.Background(), helperRoom, helperGift, 1)
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--dry-run") || strings.Contains(args, "--go") {
		t.Fatalf("dry-run参数错误：%s", args)
	}
	if strings.Contains(args, "leak") || strings.Contains(args, "private-cookie") {
		t.Fatal("凭据进入命令行")
	}
	for _, item := range cmd.Env {
		if strings.HasPrefix(item, "FISH_BILI_") {
			t.Fatalf("dry-run携带凭据环境：%s", item)
		}
	}
	if cmd.Stderr == nil || cmd.WaitDelay != 2*time.Second {
		t.Fatal("子进程隔离配置缺失")
	}
}

func TestRealCommandContainsOnlyWhitelistedEnvironment(t *testing.T) {
	t.Setenv("FISH_UNRELATED_SECRET", "must-not-pass")
	o := realOptions()
	o.Cookie = "SESSDATA=fixture; bili_jct=fixture; buvid3=fixture"
	s := newSender(t, o)
	cmd := s.buildCommand(context.Background(), helperRoom, helperGift, 2)
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--go") || strings.Contains(args, "--dry-run") || !strings.Contains(args, "--num 2") {
		t.Fatalf("真送参数错误：%s", args)
	}
	if strings.Contains(args, "fixture") {
		t.Fatal("凭据进入命令行")
	}
	var hasCookie bool
	for _, item := range cmd.Env {
		if strings.HasPrefix(item, "FISH_UNRELATED_SECRET=") {
			t.Fatal("环境未隔离")
		}
		if item == "FISH_BILI_COOKIE="+o.Cookie {
			hasCookie = true
		}
	}
	if !hasCookie {
		t.Fatal("真送未传入凭据环境")
	}
	o.Cookie, o.CookieFile = "", "cookie.txt"
	fileSender := newSender(t, o)
	var hasFile bool
	for _, item := range fileSender.buildCommand(context.Background(), helperRoom, helperGift, 1).Env {
		if item == "FISH_BILI_COOKIE_FILE=cookie.txt" {
			hasFile = true
		}
	}
	if !hasFile {
		t.Fatal("cookie文件未传入")
	}
}

func TestSendDryRunDoesNotConsumeLimit(t *testing.T) {
	s := newSender(t, DefaultOptions())
	s.command = helperCommand("sent", nil)
	for i := 0; i < 3; i++ {
		result, err := s.Send(context.Background(), helperRoom, helperGift, 1)
		if err != nil || !result.Sent || result.GiftID != helperGift || result.Num != 1 {
			t.Fatalf("dry-run结果不符：%+v %v", result, err)
		}
	}
	if s.used != 0 {
		t.Fatalf("dry-run不应计数：%d", s.used)
	}
}

func TestSendRealSuccessAndDailyLimit(t *testing.T) {
	var spawns int32
	o := realOptions()
	o.DailyLimit = 2
	s := newSender(t, o)
	s.command = helperCommand("sent", &spawns)
	for i := 0; i < 2; i++ {
		result, err := s.Send(context.Background(), helperRoom, helperGift, 1)
		if err != nil || !result.Sent {
			t.Fatalf("第%d次真送失败：%+v %v", i+1, result, err)
		}
	}
	if s.used != 2 {
		t.Fatalf("额度计数错误：%d", s.used)
	}
	result, err := s.Send(context.Background(), helperRoom, helperGift, 1)
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindFatal || result.Error == "" {
		t.Fatalf("超限未被拒绝：%+v %v", result, err)
	}
	if atomic.LoadInt32(&spawns) != 2 {
		t.Fatal("超限后仍启动子进程")
	}
}

func TestRetryableFailureReleasesSlotButFatalKeepsIt(t *testing.T) {
	var spawns int32
	o := realOptions()
	s := newSender(t, o)
	s.command = helperCommand("retryable", &spawns)
	if _, err := s.Send(context.Background(), helperRoom, helperGift, 1); err == nil {
		t.Fatal("retryable未返回错误")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != KindRetryable {
			t.Fatalf("分类错误：%v", err)
		}
	}
	if s.used != 0 {
		t.Fatalf("确定未POST的失败应释放额度：%d", s.used)
	}
	s.command = helperCommand("sent", &spawns)
	if result, err := s.Send(context.Background(), helperRoom, helperGift, 1); err != nil || !result.Sent {
		t.Fatalf("释放额度后应可重试：%+v %v", result, err)
	}

	fatalSender := newSender(t, realOptions())
	fatalSender.command = helperCommand("fatal", &spawns)
	if _, err := fatalSender.Send(context.Background(), helperRoom, helperGift, 1); err == nil {
		t.Fatal("fatal未返回错误")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != KindFatal {
			t.Fatalf("分类错误：%v", err)
		}
	}
	if fatalSender.used != 1 {
		t.Fatal("不确定失败必须保留额度")
	}
	if _, err := fatalSender.Send(context.Background(), helperRoom, helperGift, 1); err == nil {
		t.Fatal("保留额度后第二次仍应被限额拒绝")
	}
}

func TestProtocolFailuresKeepReservationAndDoNotLeak(t *testing.T) {
	for _, mode := range []string{"empty", "partial", "protocol-2", "unknown-field", "two-lines", "mismatch", "oversize", "sent-exit-1", "bad-kind", "bad-message", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			var spawns int32
			s := newSender(t, realOptions())
			s.command = helperCommand(mode, &spawns)
			result, err := s.Send(context.Background(), helperRoom, helperGift, 1)
			if mode == "stderr" {
				if err != nil || !result.Sent {
					t.Fatalf("stderr不得污染协议：%+v %v", result, err)
				}
				if s.used != 1 {
					t.Fatalf("成功应计数：%d", s.used)
				}
				return
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != KindFatal {
				t.Fatalf("协议异常应fatal：%+v %v", result, err)
			}
			if strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "private") {
				t.Fatal("错误泄露子进程输出")
			}
			if s.used != 1 {
				t.Fatalf("不确定结果必须保留额度：%d", s.used)
			}
		})
	}
}

func TestSendInputValidationDoesNotSpawn(t *testing.T) {
	var spawns int32
	s := newSender(t, realOptions())
	s.command = helperCommand("sent", &spawns)
	for _, input := range []struct {
		room, giftID string
		num          int
	}{
		{"0", helperGift, 1},
		{"abc", helperGift, 1},
		{"99999999999999999", helperGift, 1},
		{helperRoom, "0", 1},
		{helperRoom, helperGift, 0},
		{helperRoom, helperGift, 101},
	} {
		result, err := s.Send(context.Background(), input.room, input.giftID, input.num)
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != KindFatal || result.Error == "" {
			t.Fatalf("无效输入未被拒绝：%+v %v", result, err)
		}
	}
	var typed *Error
	if _, err := s.Send(nil, helperRoom, helperGift, 1); !errors.As(err, &typed) {
		t.Fatal("nil上下文未被拒绝")
	}
	if atomic.LoadInt32(&spawns) != 0 || s.used != 0 {
		t.Fatal("无效输入不得启动子进程或计数")
	}
}

func TestTimeoutAndCancelKeepReservation(t *testing.T) {
	o := realOptions()
	o.Timeout = time.Second
	s := newSender(t, o)
	s.command = helperCommand("hang", nil)
	started := time.Now()
	result, err := s.Send(context.Background(), helperRoom, helperGift, 1)
	if err == nil || !strings.Contains(result.Error, "超时或取消") {
		t.Fatalf("超时未按未知结果处理：%+v %v", result, err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("超时未生效")
	}
	if s.used != 1 {
		t.Fatal("超时必须保留额度")
	}

	cancelled := newSender(t, o)
	cancelled.command = helperCommand("hang", nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := cancelled.Send(ctx, helperRoom, helperGift, 1); err == nil {
		t.Fatal("取消未返回错误")
	}
	if cancelled.used != 1 {
		t.Fatal("取消必须保留额度")
	}
}

func TestStartFailureReleasesReservation(t *testing.T) {
	o := realOptions()
	s := newSender(t, o)
	s.command = func(ctx context.Context, room, giftID string, num int) *exec.Cmd {
		return exec.CommandContext(ctx, filepath.Join(t.TempDir(), "definitely-missing-node"))
	}
	if _, err := s.Send(context.Background(), helperRoom, helperGift, 1); err == nil {
		t.Fatal("启动失败未返回错误")
	}
	if s.used != 0 {
		t.Fatalf("未启动进程应释放额度：%d", s.used)
	}
	s.command = helperCommand("sent", nil)
	if result, err := s.Send(context.Background(), helperRoom, helperGift, 1); err != nil || !result.Sent {
		t.Fatalf("释放后应可重试：%+v %v", result, err)
	}
}

func TestDailyCounterResetsOnLocalDate(t *testing.T) {
	now := time.Date(2026, 11, 2, 23, 59, 0, 0, time.Local)
	o := realOptions()
	o.Now = func() time.Time { return now }
	s := newSender(t, o)
	s.command = helperCommand("sent", nil)
	if _, err := s.Send(context.Background(), helperRoom, helperGift, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(context.Background(), helperRoom, helperGift, 1); err == nil {
		t.Fatal("同日第二次应被限额拒绝")
	}
	now = now.Add(2 * time.Minute)
	if _, err := s.Send(context.Background(), helperRoom, helperGift, 1); err != nil {
		t.Fatalf("跨日未重置：%v", err)
	}
	if s.used != 1 {
		t.Fatalf("跨日计数错误：%d", s.used)
	}
}

func TestConcurrentSendsCannotExceedDailyLimit(t *testing.T) {
	o := realOptions()
	o.DailyLimit = 3
	s := newSender(t, o)
	var spawns int32
	s.command = helperCommand("sent", &spawns)
	var wg sync.WaitGroup
	var success, limited int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Send(context.Background(), helperRoom, helperGift, 1); err != nil {
				atomic.AddInt32(&limited, 1)
			} else {
				atomic.AddInt32(&success, 1)
			}
		}()
	}
	wg.Wait()
	if success != 3 || limited != 7 || s.used != 3 || atomic.LoadInt32(&spawns) != 3 {
		t.Fatalf("并发限额失效：success=%d limited=%d used=%d spawns=%d", success, limited, s.used, spawns)
	}
}

func TestFrameMessageValidation(t *testing.T) {
	for _, message := range []string{"", "x\n", "x\r", "x\t", strings.Repeat("鱼", 121)} {
		if safeFrameMessage(message) {
			t.Fatalf("非法消息未被拒绝：%q", message)
		}
	}
	if !safeFrameMessage(strings.Repeat("鱼", 120)) {
		t.Fatal("合法消息被拒绝")
	}
}
