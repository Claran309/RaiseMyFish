package config

// 本文件测 0.11 的 [scheduler] 段：默认值、校验边界、通道键解析、整段缺失。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"RaiseMyFish/internal/event"
)

// loadWithScheduler 写一份 TOML 并加载，DSN 走进程环境。
func loadWithScheduler(t *testing.T, body string) (Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	return Load(path, "")
}

// validLiveChannel 是合法的 live 通道段。
const validLiveChannel = `
[scheduler.channels.live]
brain = "default"
rate_per_minute = 6
burst = 2
daily_quota = 200
self_names = ["大肥鱼", "Claranの大肥鱼"]
`

// TestSchedulerDefaultsFilled 段内省略的字段用默认值补齐。
func TestSchedulerDefaultsFilled(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[scheduler]"+validLiveChannel)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	s := cfg.Scheduler
	if s == nil {
		t.Fatal("带 [scheduler] 段时不应为 nil")
	}
	if s.MaxConcurrent != 4 || s.MaxPending != 64 || s.ClaimBatch != 8 {
		t.Errorf("并发/待处理/批次默认值不符：%+v", s)
	}
	if s.PollIntervalMS != 500 || s.RetryDelayMS != 5000 || s.OperationTimeoutMS != 10000 || s.WorkerTimeoutMS != 30000 {
		t.Errorf("时间参数默认值不符：%+v", s)
	}

	p, ok := s.Channels[event.ChLive]
	if !ok {
		t.Fatalf("缺少 live 通道：%+v", s.Channels)
	}
	if p.Brain != "default" || p.RatePerMinute != 6 || p.Burst != 2 || p.DailyQuota != 200 {
		t.Errorf("live 策略解析错误：%+v", p)
	}
	if p.MinRunes != 2 {
		t.Errorf("min_runes 默认应为 2，实际 %d", p.MinRunes)
	}
	if p.ReplyOnlyMention == nil || !*p.ReplyOnlyMention {
		t.Errorf("live 未写 reply_only_mention 时应默认 true：%+v", p)
	}
	if p.ReplyMentionBack == nil || !*p.ReplyMentionBack {
		t.Errorf("live 未写 reply_mention_back 时应默认 true：%+v", p)
	}
	if p.MaxReplyRunes != 40 {
		t.Errorf("live 未写 max_reply_runes 时应默认 40，实际 %d", p.MaxReplyRunes)
	}
	if len(p.SelfNames) != 2 || p.SelfNames[0] != "大肥鱼" {
		t.Errorf("self_names 解析错误：%+v", p.SelfNames)
	}
}

// TestSchedulerMentionBackExplicitValues 显式关闭/自定义上限不被默认值覆盖。
func TestSchedulerMentionBackExplicitValues(t *testing.T) {
	body := "[scheduler.channels.live]\nbrain='default'\nrate_per_minute=6\nburst=1\ndaily_quota=10\nreply_mention_back=false\nmax_reply_runes=30\n"
	cfg, err := loadWithScheduler(t, body)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	p := cfg.Scheduler.Channels[event.ChLive]
	if p.ReplyMentionBack == nil || *p.ReplyMentionBack {
		t.Fatalf("显式 false 被覆盖：%+v", p.ReplyMentionBack)
	}
	if p.MaxReplyRunes != 30 {
		t.Fatalf("显式 max_reply_runes 被覆盖：%d", p.MaxReplyRunes)
	}
}

// TestSchedulerReplyOnlyMentionExplicitFalse 显式关闭后不再要求 self_names。
func TestSchedulerReplyOnlyMentionExplicitFalse(t *testing.T) {
	body := "[scheduler.channels.live]\nbrain='default'\nrate_per_minute=6\nburst=1\ndaily_quota=10\nreply_only_mention=false\n"
	cfg, err := loadWithScheduler(t, body)
	if err != nil {
		t.Fatalf("显式关闭不应要求 self_names：%v", err)
	}
	p := cfg.Scheduler.Channels[event.ChLive]
	if p.ReplyOnlyMention == nil || *p.ReplyOnlyMention {
		t.Fatalf("显式 false 被覆盖：%+v", p.ReplyOnlyMention)
	}
}

// TestSchedulerAbsentMeansCollectOnly 没有 [scheduler] 段 → nil，保持只采集。
func TestSchedulerAbsentMeansCollectOnly(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[log]\nlevel='info'\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.Scheduler != nil {
		t.Errorf("缺少 [scheduler] 段时应为 nil，实际 %+v", cfg.Scheduler)
	}
}

// TestSchedulerDefaultsDoNotOverride 显式值不被默认值覆盖。
func TestSchedulerDefaultsDoNotOverride(t *testing.T) {
	body := "[scheduler]\nmax_concurrent = 9\nmax_pending = 99\nclaim_batch = 7\npoll_interval_ms = 250\n" + validLiveChannel
	cfg, err := loadWithScheduler(t, body)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	s := cfg.Scheduler
	if s.MaxConcurrent != 9 || s.MaxPending != 99 || s.ClaimBatch != 7 || s.PollIntervalMS != 250 {
		t.Errorf("显式值被覆盖：%+v", s)
	}
}

// TestSchedulerValidationRejects 非法配置在 Load 阶段就被拒。
func TestSchedulerValidationRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"并发超上限", "[scheduler]\nmax_concurrent = 101\n", "并发"},
		{"待处理小于并发", "[scheduler]\nmax_concurrent = 8\nmax_pending = 4\n", "待处理"},
		{"批次超上限", "[scheduler]\nclaim_batch = 101\n", "批次"},
		{"轮询非正", "[scheduler]\nmax_concurrent = 4\nmax_pending = 8\nclaim_batch = 2\npoll_interval_ms = 1\n", ""},
		{"没有通道", "[scheduler]\n", "通道"},
		{"brain 为空", "[scheduler.channels.live]\nrate_per_minute = 6\nburst = 1\ndaily_quota = 10\n", "brain"},
		{"rate 为 0", "[scheduler.channels.live]\nbrain = \"default\"\nburst = 1\ndaily_quota = 10\n", "rate_per_minute"},
		{"burst 为 0", "[scheduler.channels.live]\nbrain = \"default\"\nrate_per_minute = 6\ndaily_quota = 10\n", "burst"},
		{"quota 为 0", "[scheduler.channels.live]\nbrain = \"default\"\nrate_per_minute = 6\nburst = 1\n", "daily_quota"},
		{"未知通道", "[scheduler.channels.telegram]\nbrain = \"default\"\nrate_per_minute = 6\nburst = 1\ndaily_quota = 10\n", "通道"},
		{"mention缺名字", "[scheduler.channels.live]\nbrain = \"default\"\nrate_per_minute = 6\nburst = 1\ndaily_quota = 10\nreply_only_mention = true\n", "self_names"},
		{"mention名字含@", "[scheduler.channels.live]\nbrain = \"default\"\nrate_per_minute = 6\nburst = 1\ndaily_quota = 10\nself_names = [\"@鱼\"]\n", "self_names"},
		{"mention用于非live", "[scheduler.channels.comment]\nbrain = \"default\"\nrate_per_minute = 6\nburst = 1\ndaily_quota = 10\nreply_only_mention = true\nself_names = [\"大肥鱼\"]\n", "reply_only_mention"},
		{"mention回带用于非live", "[scheduler.channels.comment]\nbrain = \"default\"\nrate_per_minute = 6\nburst = 1\ndaily_quota = 10\nreply_mention_back = true\n", "reply_mention_back"},
		{"回复上限超40", "[scheduler.channels.live]\nbrain = \"default\"\nrate_per_minute = 6\nburst = 1\ndaily_quota = 10\nmax_reply_runes = 41\n", "max_reply_runes"},
		{"回复上限为负", "[scheduler.channels.live]\nbrain = \"default\"\nrate_per_minute = 6\nburst = 1\ndaily_quota = 10\nmax_reply_runes = -1\n", "max_reply_runes"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadWithScheduler(t, tc.body)
			if err == nil {
				t.Fatal("非法配置未被拒绝")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息 %q 不含 %q", err.Error(), tc.want)
			}
		})
	}
}

// TestSchedulerValidateNil 未启用时校验直接通过。
func TestSchedulerValidateNil(t *testing.T) {
	var s *Scheduler
	if err := s.Validate(); err != nil {
		t.Errorf("nil 配置应通过校验：%v", err)
	}
	s.ApplyDefaults() // nil 也不能 panic
}
