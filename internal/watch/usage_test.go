package watch

// 本文件测视觉记账适配（O08）：写 llm_usage 的口径、unknown 表达与失败降级。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type fakeUsageRepo struct {
	mu   sync.Mutex
	rows []store.Usage
	err  error
}

func (f *fakeUsageRepo) RecordUsage(_ context.Context, u store.Usage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, u)
	return f.err
}

func (f *fakeUsageRepo) snapshot() []store.Usage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Usage(nil), f.rows...)
}

func usageIntPtr(n int) *int           { return &n }
func usageFloatPtr(v float64) *float64 { return &v }

// TestUsageSinkForRecordsLiveRow 视觉调用写进 live 通道的账本行，费用按报价算。
func TestUsageSinkForRecordsLiveRow(t *testing.T) {
	repo := &fakeUsageRepo{}
	sink := UsageSinkFor(repo, "vision-model", usageFloatPtr(1), usageFloatPtr(2), zap.NewNop())
	if sink == nil {
		t.Fatal("有账本时应返回 sink")
	}

	sink(context.Background(), VisionUsage{InputTokens: usageIntPtr(100), OutputTokens: usageIntPtr(20)}, true, 120*time.Millisecond)

	rows := repo.snapshot()
	if len(rows) != 1 {
		t.Fatalf("应写 1 行：%+v", rows)
	}
	row := rows[0]
	if row.Channel != event.ChLive || row.Model != "vision-model" || !row.OK {
		t.Fatalf("基础字段不符：%+v", row)
	}
	// (100*1 + 20*2) / 1e6 = 0.00014
	if row.CostUSD == nil || *row.CostUSD < 0.0001399 || *row.CostUSD > 0.0001401 {
		t.Fatalf("费用不符：%v", row.CostUSD)
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("账本行不合法：%v", err)
	}
}

// TestUsageSinkForUnknownCost 缺报价或缺 token → CostUSD=nil（unknown），不是 0。
func TestUsageSinkForUnknownCost(t *testing.T) {
	cases := []struct {
		name    string
		in, out *float64
		usage   VisionUsage
	}{
		{"没有报价", nil, nil, VisionUsage{InputTokens: usageIntPtr(100), OutputTokens: usageIntPtr(20)}},
		{"没有 token", usageFloatPtr(1), usageFloatPtr(2), VisionUsage{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeUsageRepo{}
			UsageSinkFor(repo, "m", tc.in, tc.out, zap.NewNop())(context.Background(), tc.usage, true, time.Millisecond)
			rows := repo.snapshot()
			if len(rows) != 1 {
				t.Fatalf("应写 1 行：%+v", rows)
			}
			if rows[0].CostUSD != nil {
				t.Fatalf("未知费用应记 nil，实际 %v", *rows[0].CostUSD)
			}
		})
	}
}

// TestUsageSinkForNilRepo 没有账本时返回 nil，调用方无需判空。
func TestUsageSinkForNilRepo(t *testing.T) {
	if sink := UsageSinkFor(nil, "m", nil, nil, zap.NewNop()); sink != nil {
		t.Fatal("nil 账本应返回 nil sink")
	}
}

// TestUsageSinkForSwallowsWriteError 账本写失败只 Warn，不 panic、不阻塞。
func TestUsageSinkForSwallowsWriteError(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	repo := &fakeUsageRepo{err: errors.New("账本写不进去")}
	sink := UsageSinkFor(repo, "vision-model", nil, nil, zap.New(core))

	sink(context.Background(), VisionUsage{}, false, time.Millisecond)

	if logs.FilterMessage("视觉调用记账失败").Len() != 1 {
		t.Fatalf("缺少记账失败日志：%v", logs.All())
	}
	rows := repo.snapshot()
	if len(rows) != 1 || rows[0].OK {
		t.Fatalf("失败调用也要尝试记账且 ok=false：%+v", rows)
	}
}

// TestUsageSinkForCanceledContext 父 ctx 已取消也要能记账（记账脱离取消）。
func TestUsageSinkForCanceledContext(t *testing.T) {
	repo := &fakeUsageRepo{}
	sink := UsageSinkFor(repo, "m", nil, nil, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sink(ctx, VisionUsage{}, true, time.Millisecond)

	if rows := repo.snapshot(); len(rows) != 1 {
		t.Fatalf("取消后仍应记账：%+v", rows)
	}
}
