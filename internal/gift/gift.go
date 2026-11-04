// Package gift 是对 tools/bili-whale/gift.mjs 的薄封装：
// 只负责参数校验、子进程隔离、超时/取消、JSONL 解析和失败分类，
// 平台交互和资金安全判断全部留在 Node 脚本内。
package gift

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Kind 是送礼失败的可重试性；语义与 sender 通道保持一致。
type Kind string

const (
	KindRetryable Kind = "retryable"
	KindFatal     Kind = "fatal"
)

// Error 只携带固定中文消息；底层原因仅用于 errors.Is/日志，不对外拼接。
type Error struct {
	Kind    Kind
	Message string
	// Uncertain 表示请求可能已到达平台但结果未知（超时/协议异常/POST 后网络失败）。
	// 此时额度不退还，避免「可能已送出」的礼物被重复扣费。
	Uncertain bool
	Cause     error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "送礼失败"
}

func (e *Error) Unwrap() error { return e.Cause }

// Sender 是送礼通道的最小接口。
type Sender interface {
	Send(ctx context.Context, room string, giftID string, num int) (Result, error)
}

// Result 是一次送礼调用的结果；dry-run 成功时 Sent 也为 true（表示脚本确认「将会发送」）。
type Result struct {
	Sent   bool
	GiftID string
	Num    int
	Error  string
	// PendingReview 为 true 表示请求可能已送出但结果未知：额度保留，需人工核对账目。
	PendingReview bool
}

// Options 是礼物发送的部署配置；凭据不参与 JSON 序列化。
type Options struct {
	NodePath   string
	ScriptPath string
	Cookie     string `json:"-"`
	CookieFile string `json:"-"`
	Timeout    time.Duration
	// DryRun 为 true 时只向脚本传 --dry-run，不携带任何凭据。
	DryRun bool
	// DailyLimit 是每个本地自然日允许真实送出的礼物总数；dry-run 不计数。
	DailyLimit int
	// MaxPrice 是礼物单价上限（price 单位，100=1电池）。
	MaxPrice int
	// MaxNum 是单次送礼数量上限。
	MaxNum int
	// AllowedRooms 是房间白名单；空表示不限制。
	AllowedRooms []string
	// Quota 是可选的每日额度仓储；nil 时使用进程内计数（适合长驻进程）。
	// 一次性命令必须注入持久化实现，否则每次启动都会重置 daily_limit。
	Quota QuotaStore
	// Now 仅用于测试跨日重置；nil 时使用 time.Now。
	Now func() time.Time
}

// DefaultOptions 与 config 默认值同一口径：默认 dry-run、每日最多1个。
func DefaultOptions() Options {
	return Options{
		NodePath:   "node",
		ScriptPath: "tools/bili-whale/gift.mjs",
		Timeout:    30 * time.Second,
		DryRun:     true,
		DailyLimit: 1,
		MaxPrice:   100,
		MaxNum:     10,
	}
}

// NodeSender 串行化每日额度并在每次调用时启动一个一次性 Node 进程。
type NodeSender struct {
	options      Options
	command      func(context.Context, string, string, int) *exec.Cmd
	allowedRooms map[string]struct{}

	mu   sync.Mutex
	day  string
	used int
}

var giftIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)

// maxSafeInteger 与 Node 侧 JSON 数字上限一致，避免子进程丢失精度。
const maxSafeInteger = 9007199254740991

func validGiftID(value string) bool {
	if !giftIDPattern.MatchString(value) {
		return false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return err == nil && number <= maxSafeInteger
}

// New 只做静态校验，不读取凭据文件、启动 Node 或访问平台。
func New(o Options) (*NodeSender, error) {
	if strings.TrimSpace(o.NodePath) == "" || strings.TrimSpace(o.ScriptPath) == "" ||
		strings.ContainsAny(o.NodePath+o.ScriptPath+o.Cookie+o.CookieFile, "\r\n\x00") ||
		o.Timeout < time.Second || o.Timeout > 10*time.Minute ||
		o.DailyLimit < 1 || o.DailyLimit > 100 ||
		o.MaxPrice < 1 || o.MaxPrice > 10000 ||
		o.MaxNum < 1 || o.MaxNum > 100 {
		return nil, &Error{Kind: KindFatal, Message: "送礼运行时或限额配置无效"}
	}
	allowed := make(map[string]struct{}, len(o.AllowedRooms))
	for _, room := range o.AllowedRooms {
		if !validGiftID(room) {
			return nil, &Error{Kind: KindFatal, Message: "送礼房间白名单无效"}
		}
		allowed[room] = struct{}{}
	}
	o.AllowedRooms = append([]string(nil), o.AllowedRooms...)
	if o.DryRun {
		// dry-run 一律不传凭据，即使调用方误配也不泄露。
		o.Cookie, o.CookieFile = "", ""
	} else if o.Cookie == "" && o.CookieFile == "" {
		return nil, &Error{Kind: KindFatal, Message: "真实送礼需要B站凭据"}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &NodeSender{options: o, allowedRooms: allowed}
	s.command = s.buildCommand
	return s, nil
}

func (s *NodeSender) buildCommand(ctx context.Context, room, giftID string, num int) *exec.Cmd {
	mode := "--dry-run"
	if !s.options.DryRun {
		mode = "--go"
	}
	args := []string{s.options.ScriptPath, "--room", room, "--gift-id", giftID, "--num", strconv.Itoa(num),
		"--max-price", strconv.Itoa(s.options.MaxPrice), "--max-num", strconv.Itoa(s.options.MaxNum), mode}
	// #nosec G204 -- Node与脚本路径来自可信部署配置；逐参数传递且不经shell，平台ID已在Send中限制为十进制。
	cmd := exec.CommandContext(ctx, s.options.NodePath, args...)
	cmd.Env = []string{}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "PATHEXT", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if !s.options.DryRun {
		if s.options.Cookie != "" {
			cmd.Env = append(cmd.Env, "FISH_BILI_COOKIE="+s.options.Cookie)
		} else {
			cmd.Env = append(cmd.Env, "FISH_BILI_COOKIE_FILE="+s.options.CookieFile)
		}
	}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// reserve 在启动真实送礼前预占额度；并发调用不会突破每日上限。
func (s *NodeSender) reserve(day string, num int) error {
	if s.options.Quota != nil {
		return s.options.Quota.Reserve(day, num, s.options.DailyLimit)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if day != s.day {
		s.day = day
		s.used = 0
	}
	if s.used+num > s.options.DailyLimit {
		return &Error{Kind: KindFatal, Message: "送礼超过每日限额"}
	}
	s.used += num
	return nil
}

// release 只用于确定未送出的失败；不确定时宁可占用额度也不重复扣费。
func (s *NodeSender) release(day string, num int) {
	if s.options.Quota != nil {
		s.options.Quota.Release(day, num)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if day != s.day {
		return
	}
	s.used -= num
	if s.used < 0 {
		s.used = 0
	}
}

func (s *NodeSender) Send(ctx context.Context, room string, giftID string, num int) (Result, error) {
	result := Result{GiftID: giftID, Num: num}
	fail := func(kind Kind, message string) (Result, error) {
		result.Error = message
		return result, &Error{Kind: kind, Message: message}
	}
	if ctx == nil {
		return fail(KindFatal, "缺少调用上下文")
	}
	if !validGiftID(room) || !validGiftID(giftID) || num < 1 || num > s.options.MaxNum {
		return fail(KindFatal, "房间号/礼物ID/数量无效")
	}
	if len(s.allowedRooms) > 0 {
		if _, ok := s.allowedRooms[room]; !ok {
			return fail(KindFatal, "房间不在送礼白名单")
		}
	}
	if ctx.Err() != nil {
		return fail(KindFatal, "送礼已取消")
	}
	reserved := false
	day := s.options.Now().Format("2006-01-02")
	if !s.options.DryRun {
		if err := s.reserve(day, num); err != nil {
			var typed *Error
			if errors.As(err, &typed) {
				return fail(typed.Kind, typed.Message)
			}
			return fail(KindFatal, "送礼每日限额检查失败")
		}
		reserved = true
	}
	frame, started, err := s.execute(ctx, room, giftID, num)
	if err != nil {
		// 只有「确定没送出去」才退额度：启动失败或脚本明确回报（含 fatal 平台拒绝）。
		// 结果未知时保留额度并标记待核对，避免同一份额度被重复使用、造成重复扣费。
		uncertain := started
		var typed *Error
		if errors.As(err, &typed) {
			uncertain = typed.Uncertain
		}
		if reserved {
			if uncertain {
				result.PendingReview = true
			} else {
				s.release(day, num)
			}
		}
		result.Error = err.Error()
		return result, err
	}
	result.Sent = true
	result.GiftID = frame.GiftID
	result.Num = frame.Num
	return result, nil
}

type frame struct {
	Protocol  int    `json:"protocol"`
	Type      string `json:"type"`
	GiftID    string `json:"gift_id"`
	Num       int    `json:"num"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Uncertain bool   `json:"uncertain"`
}

const maxOutput = 64 * 1024

// protocolError 都是「已启动子进程但拿不到可信结果」，一律视为结果未知。
func protocolError(cause error) error {
	return &Error{Kind: KindFatal, Message: "送礼脚本输出无效", Uncertain: true, Cause: cause}
}

// execute 启动一次性子进程并严格解析单行 JSONL；started 用于判断能否安全释放额度。
func (s *NodeSender) execute(parent context.Context, room, giftID string, num int) (frame, bool, error) {
	ctx, cancel := context.WithTimeout(parent, s.options.Timeout)
	defer cancel()
	cmd := s.command(ctx, room, giftID, num)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return frame{}, false, &Error{Kind: KindFatal, Message: "送礼脚本启动失败", Cause: err}
	}
	if err := cmd.Start(); err != nil {
		return frame{}, false, &Error{Kind: KindFatal, Message: "送礼脚本启动失败", Cause: err}
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxOutput+1))
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		// 真实模式下超时/取消发生在POST之后时结果未知，按fatal处理且不自动重试。
		return frame{}, true, &Error{Kind: KindFatal, Message: "送礼子进程超时或取消，结果可能未知", Uncertain: true, Cause: errors.Join(ctx.Err(), readErr, waitErr)}
	}
	if readErr != nil || len(data) > maxOutput {
		return frame{}, true, &Error{Kind: KindFatal, Message: "送礼子进程输出失败", Uncertain: true, Cause: errors.Join(readErr, waitErr)}
	}
	line := strings.TrimSuffix(string(data), "\n")
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return frame{}, true, protocolError(nil)
	}
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.DisallowUnknownFields()
	var f frame
	if err := decoder.Decode(&f); err != nil {
		return frame{}, true, protocolError(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF || f.Protocol != 1 {
		return frame{}, true, protocolError(nil)
	}
	switch f.Type {
	case "sent":
		// 声明成功却以非零码退出属于自相矛盾，按未确认处理。
		if waitErr != nil || f.GiftID != giftID || f.Num != num || f.Kind != "" || f.Message != "" || f.Uncertain {
			return frame{}, true, protocolError(waitErr)
		}
		return f, true, nil
	case "error":
		if (f.Kind != string(KindRetryable) && f.Kind != string(KindFatal)) || f.GiftID != "" || f.Num != 0 || !safeFrameMessage(f.Message) {
			return frame{}, true, protocolError(nil)
		}
		return frame{}, true, &Error{Kind: Kind(f.Kind), Message: f.Message, Uncertain: f.Uncertain}
	default:
		return frame{}, true, protocolError(nil)
	}
}

func safeFrameMessage(message string) bool {
	return message != "" && utf8.ValidString(message) && utf8.RuneCountInString(message) <= 120 && !strings.ContainsAny(message, "\x00\r\n\t")
}

var _ Sender = (*NodeSender)(nil)
