package embedding

// 本文件测 F07 的账本 sink：purpose 从 context 带入、关联 ID 安全化、失败也记账。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/llmmeta"
	"RaiseMyFish/internal/store"
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

func intPtr(v int) *int { return &v }

// TestUsageSinkForAnnotatedCall 带用途与关联 ID 的调用落成一条合法账本行。
func TestUsageSinkForAnnotatedCall(t *testing.T) {
	repo := &fakeUsageRepo{}
	sink := UsageSinkFor(repo, "text-embedding-3", nil)
	if sink == nil {
		t.Fatal("有账本时应返回 sink")
	}
	ctx := llmmeta.WithCall(context.Background(), llmmeta.Call{
		Purpose: llmmeta.PurposeEmbedIndex, CorrelationID: "knowledge",
	})
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	sink(ctx, Usage{At: at, InputTokens: intPtr(120), CachedInputTokens: intPtr(20), Milliseconds: 42, OK: true})

	rows := repo.snapshot()
	if len(rows) != 1 {
		t.Fatalf("应写 1 行：%+v", rows)
	}
	row := rows[0]
	if row.Purpose != string(llmmeta.PurposeEmbedIndex) || row.CorrelationID != "knowledge" {
		t.Fatalf("用途/关联 ID 不符：%+v", row)
	}
	if row.Channel != "" {
		t.Fatalf("embedding 不属于对话通道，实际 %q", row.Channel)
	}
	if row.Model != "text-embedding-3" || row.At != at || row.Milliseconds != 42 || !row.OK {
		t.Fatalf("基础字段不符：%+v", row)
	}
	if row.InputTokens == nil || *row.InputTokens != 120 || row.CachedInputTokens == nil || *row.CachedInputTokens != 20 {
		t.Fatalf("token 字段不符：%+v", row)
	}
	// 没有 embedding 报价配置：费用保持未知，不能补 0。
	if row.CostUSD != nil {
		t.Fatalf("费用应保持 unknown，实际 %v", *row.CostUSD)
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("账本行不合法：%v", err)
	}
}

// TestUsageSinkForDefaults 没有用途标注时记 unknown；不安全关联 ID 直接丢弃。
func TestUsageSinkForDefaults(t *testing.T) {
	repo := &fakeUsageRepo{}
	UsageSinkFor(repo, "m", nil)(context.Background(), Usage{At: time.Now(), OK: false})

	rows := repo.snapshot()
	if len(rows) != 1 {
		t.Fatalf("应写 1 行：%+v", rows)
	}
	if rows[0].Purpose != string(llmmeta.PurposeUnknown) {
		t.Fatalf("缺省用途应为 unknown，实际 %q", rows[0].Purpose)
	}
	if rows[0].CorrelationID != "" {
		t.Fatalf("没有关联 ID 时应为空，实际 %q", rows[0].CorrelationID)
	}
	if rows[0].OK {
		t.Fatal("失败尝试应记 ok=false")
	}

	ctx := llmmeta.WithCall(context.Background(), llmmeta.Call{CorrelationID: "小明\n第二行"})
	repo2 := &fakeUsageRepo{}
	UsageSinkFor(repo2, "m", nil)(ctx, Usage{At: time.Now(), OK: true})
	if got := repo2.snapshot()[0].CorrelationID; got != "" {
		t.Fatalf("不安全关联 ID 未丢弃：%q", got)
	}
}

// TestUsageSinkForNilRepo 没有账本时返回 nil，调用方无需判空。
func TestUsageSinkForNilRepo(t *testing.T) {
	if sink := UsageSinkFor(nil, "m", nil); sink != nil {
		t.Fatal("nil 账本应返回 nil sink")
	}
}

// TestUsageSinkForReportsError 记账失败通过回调上报，不 panic。
func TestUsageSinkForReportsError(t *testing.T) {
	boom := errors.New("账本写不进去")
	repo := &fakeUsageRepo{err: boom}
	var got error
	sink := UsageSinkFor(repo, "m", func(err error) { got = err })
	sink(context.Background(), Usage{At: time.Now(), OK: true})
	if !errors.Is(got, boom) {
		t.Fatalf("错误未上报：%v", got)
	}
}

// TestUsageSinkForDetachedContext 父 ctx 已取消也要记账（账本脱离取消，但有 2s 上限）。
func TestUsageSinkForDetachedContext(t *testing.T) {
	repo := &fakeUsageRepo{}
	sink := UsageSinkFor(repo, "m", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink(ctx, Usage{At: time.Now(), OK: true})
	if len(repo.snapshot()) != 1 {
		t.Fatal("取消后仍应记账")
	}
}
