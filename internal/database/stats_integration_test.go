//go:build integration

package database

// 0.10d：Stats 的真实 PG 聚合测试（口径、多通道、空库、时间窗口边界、心跳）。

import (
	"errors"
	"math"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// seedEvent 直接插入一条事件，避免走 Insert 的状态校验（这里要造 skipped 等历史状态）。
func seedEvent(t *testing.T, db *gorm.DB, id, channel, dir, status string, at time.Time) {
	t.Helper()
	err := db.Exec(`INSERT INTO events(id,channel,dir,session,text,at,status) VALUES(?,?,?,?,?,?,?)`,
		id, channel, dir, "sess-"+channel, "内容", at, status).Error
	if err != nil {
		t.Fatalf("插入事件 %s 失败：%v", id, err)
	}
}

// seedSend 插入一条发送审计；ok 为 nil 表示「结果未知」。
func seedSend(t *testing.T, db *gorm.DB, eventID, channel string, ok *bool, at time.Time) {
	t.Helper()
	err := db.Exec(`INSERT INTO send_log(event_id,channel,ok,verified,msg_id,at) VALUES(?,?,?,?,?,?)`,
		eventID, channel, ok, ok != nil && *ok, "msg", at).Error
	if err != nil {
		t.Fatalf("插入发送审计失败：%v", err)
	}
}

// seedUsage 插入一条用量记录。
func seedUsage(t *testing.T, db *gorm.DB, at time.Time, cost float64) {
	t.Helper()
	if err := db.Exec(`INSERT INTO llm_usage(at,channel,model,in_tokens,out_tokens,cost_usd,ms,ok) VALUES(?,?,?,?,?,?,?,?)`,
		at, "live", "test", 10, 20, cost, 5, true).Error; err != nil {
		t.Fatalf("插入用量失败：%v", err)
	}
}

func boolPtr(v bool) *bool { return &v }

// TestStatsCountsRealPostgres 口径验证：收到/跳过/回复/失败/费用 + 多通道细分。
func TestStatsCountsRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	stats, err := NewStats(db)
	if err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	// 窗口内：live 两条（其中一条被跳过）、comment 一条
	seedEvent(t, db, "live:r:1", "live", "in", "pending", from.Add(10*time.Minute))
	seedEvent(t, db, "live:r:2", "live", "in", "skipped", from.Add(20*time.Minute))
	seedEvent(t, db, "comment:c:1", "comment", "in", "pending", from.Add(30*time.Minute))
	// 窗口边界：左闭右开，from 计入、to 排除
	seedEvent(t, db, "live:r:left", "live", "in", "pending", from)
	seedEvent(t, db, "live:r:right", "live", "in", "pending", to)
	// 早于窗口
	seedEvent(t, db, "live:r:early", "live", "in", "pending", from.Add(-time.Minute))
	// out 方向不计入「收到」
	seedEvent(t, db, "live:r:out", "live", "out", "done", from.Add(15*time.Minute))

	// 回复：同一事件重试两次只算一条
	seedSend(t, db, "live:r:1", "live", boolPtr(true), from.Add(12*time.Minute))
	seedSend(t, db, "live:r:1", "live", boolPtr(true), from.Add(13*time.Minute))
	seedSend(t, db, "comment:c:1", "comment", boolPtr(true), from.Add(31*time.Minute))
	// 失败：channel 为空时回落到 events.channel
	seedSend(t, db, "live:r:2", "", boolPtr(false), from.Add(21*time.Minute))
	// 结果未知（ok IS NULL）既不算回复也不算失败
	seedSend(t, db, "comment:c:1", "comment", nil, from.Add(32*time.Minute))
	// 窗口内的入向事件、发送时刻在窗口外：新口径按**事件时间**归属，仍算这条被回复了
	seedSend(t, db, "live:r:left", "live", boolPtr(true), from.Add(-time.Minute))

	// 费用：窗口内 0.01+0.02，窗口外 9.99 不计
	seedUsage(t, db, from.Add(time.Minute), 0.01)
	seedUsage(t, db, from.Add(2*time.Minute), 0.02)
	seedUsage(t, db, from.Add(-time.Minute), 9.99)

	got, err := stats.Counts(ctx, from, to)
	if err != nil {
		t.Fatalf("Counts 失败：%v", err)
	}

	// 收到 4：live:r:1、live:r:2、comment:c:1、live:r:left（from 边界计入）
	if got.Received != 4 {
		t.Errorf("Received = %d，期望 4（[from,to) + 仅 dir=in）", got.Received)
	}
	if got.Skipped != 1 {
		t.Errorf("Skipped = %d，期望 1", got.Skipped)
	}
	if got.Replied != 3 {
		t.Errorf("Replied = %d，期望 3（按事件时间归属：live:r:1 去重 1 条、comment:c:1、live:r:left）", got.Replied)
	}
	if got.Failed != 1 {
		t.Errorf("Failed = %d，期望 1", got.Failed)
	}
	if got.CostUSD == nil {
		t.Fatal("CostUSD 为 nil，期望 0.03")
	}
	if math.Abs(*got.CostUSD-0.03) > 1e-9 {
		t.Errorf("CostUSD = %v，期望 0.03", *got.CostUSD)
	}

	// 通道细分：总量必须等于各通道之和
	live, ok := got.Channels["live"]
	if !ok {
		t.Fatalf("缺少 live 通道：%+v", got.Channels)
	}
	if live.Received != 3 || live.Skipped != 1 || live.Replied != 2 || live.Failed != 1 {
		t.Errorf("live 细分不符：%+v", live)
	}
	comment, ok := got.Channels["comment"]
	if !ok {
		t.Fatalf("缺少 comment 通道：%+v", got.Channels)
	}
	if comment.Received != 1 || comment.Replied != 1 || comment.Skipped != 0 || comment.Failed != 0 {
		t.Errorf("comment 细分不符：%+v", comment)
	}

	var sumReceived, sumReplied int
	for _, c := range got.Channels {
		sumReceived += c.Received
		sumReplied += c.Replied
	}
	if sumReceived != got.Received || sumReplied != got.Replied {
		t.Errorf("总量与细分不一致：received %d/%d replied %d/%d",
			got.Received, sumReceived, got.Replied, sumReplied)
	}
}

// TestStatsRepliedOnlyCountsInWindowInbound 回复数只算「窗口内入向事件」的发送记录。
//
// 复现 bug：窗口外的事件、出向事件的 send_log 也曾被算进 Replied，
// 导致「回复 18 > 收到-跳过 15」，成功率爆百。
func TestStatsRepliedOnlyCountsInWindowInbound(t *testing.T) {
	db, ctx := repositoryDB(t)
	stats, err := NewStats(db)
	if err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	// 窗口内的入向事件（应计入）
	seedEvent(t, db, "live:r:in1", "live", "in", "done", from.Add(10*time.Minute))
	// 窗口外的入向事件（发送记录在窗口内，也不能算）
	seedEvent(t, db, "live:r:early", "live", "in", "done", from.Add(-time.Hour))
	// 出向事件（我们自己的回复行，不能算「回复了别人」）
	seedEvent(t, db, "live:r:out", "live", "out", "done", from.Add(20*time.Minute))

	for _, id := range []string{"live:r:in1", "live:r:early", "live:r:out"} {
		seedSend(t, db, id, "live", boolPtr(true), from.Add(30*time.Minute))
	}

	got, err := stats.Counts(ctx, from, to)
	if err != nil {
		t.Fatalf("Counts 失败：%v", err)
	}
	if got.Replied != 1 {
		t.Errorf("Replied = %d，期望 1（只算窗口内 dir=in 的事件）", got.Replied)
	}
	if got.Received != 1 {
		t.Errorf("Received = %d，期望 1", got.Received)
	}
	// 关键不变式：回复数不超过「收到-跳过」
	if got.Replied > got.Received-got.Skipped {
		t.Errorf("Replied %d > Received-Skipped %d，成功率会爆百", got.Replied, got.Received-got.Skipped)
	}
}

// TestStatsFailedOnlyCountsInWindowInbound Failed 与 Replied 口径对称：
// 只算「窗口内入向事件」的 ok IS FALSE 去重数。
func TestStatsFailedOnlyCountsInWindowInbound(t *testing.T) {
	db, ctx := repositoryDB(t)
	stats, err := NewStats(db)
	if err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	// 窗口内入向事件 + 失败发送（应计入）
	seedEvent(t, db, "live:f:in1", "live", "in", "dead", from.Add(10*time.Minute))
	// 窗口外入向事件 + 失败发送（不应计入）
	seedEvent(t, db, "live:f:early", "live", "in", "dead", from.Add(-time.Hour))
	// 出向事件 + 失败发送（不应计入）
	seedEvent(t, db, "live:f:out", "live", "out", "dead", from.Add(20*time.Minute))

	for _, id := range []string{"live:f:in1", "live:f:early", "live:f:out"} {
		seedSend(t, db, id, "live", boolPtr(false), from.Add(30*time.Minute))
	}

	got, err := stats.Counts(ctx, from, to)
	if err != nil {
		t.Fatalf("Counts 失败：%v", err)
	}
	if got.Failed != 1 {
		t.Errorf("Failed = %d，期望 1（只算窗口内 dir=in 的事件）", got.Failed)
	}
}

// TestStatsEmptyDatabase 空库：全 0、费用未知（nil）、无通道。
func TestStatsEmptyDatabase(t *testing.T) {
	db, ctx := repositoryDB(t)
	stats, err := NewStats(db)
	if err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	got, err := stats.Counts(ctx, from, from.Add(time.Hour))
	if err != nil {
		t.Fatalf("Counts 失败：%v", err)
	}
	if got.Received != 0 || got.Replied != 0 || got.Skipped != 0 || got.Failed != 0 {
		t.Errorf("空库计数应全 0，实际 %+v", got)
	}
	if got.CostUSD != nil {
		t.Errorf("空库费用应为 nil（未知），实际 %v", *got.CostUSD)
	}
	if len(got.Channels) != 0 {
		t.Errorf("空库不应有通道细分，实际 %+v", got.Channels)
	}

	lastSeen, err := stats.LastSeen(ctx)
	if err != nil {
		t.Fatalf("LastSeen 失败：%v", err)
	}
	if len(lastSeen) != 0 {
		t.Errorf("空库心跳应为空，实际 %+v", lastSeen)
	}
}

// TestStatsLastSeenRealPostgres 心跳只取 dir=in 的每通道最晚时间。
func TestStatsLastSeenRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	stats, err := NewStats(db)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	seedEvent(t, db, "live:h:1", "live", "in", "pending", base)
	seedEvent(t, db, "live:h:2", "live", "in", "pending", base.Add(5*time.Minute))
	seedEvent(t, db, "comment:h:1", "comment", "in", "pending", base.Add(time.Minute))
	// out 事件更晚，但不能刷新心跳
	seedEvent(t, db, "live:h:out", "live", "out", "done", base.Add(time.Hour))

	got, err := stats.LastSeen(ctx)
	if err != nil {
		t.Fatalf("LastSeen 失败：%v", err)
	}
	if want := base.Add(5 * time.Minute); !got["live"].Equal(want) {
		t.Errorf("live 心跳 = %v，期望 %v（不看 out 事件）", got["live"], want)
	}
	if want := base.Add(time.Minute); !got["comment"].Equal(want) {
		t.Errorf("comment 心跳 = %v，期望 %v", got["comment"], want)
	}
	if len(got) != 2 {
		t.Errorf("通道数 = %d，期望 2：%+v", len(got), got)
	}
}

// TestStatsRejectsBadInput 非法参数在查询前被拒绝。
func TestStatsRejectsBadInput(t *testing.T) {
	db, ctx := repositoryDB(t)
	stats, err := NewStats(db)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	if _, err := stats.Counts(ctx, from.Add(time.Hour), from); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("from > to 应 ErrInvalid，实际 %v", err)
	}
	//nolint:staticcheck // 刻意传 nil 验证防御边界（SA1012）
	if _, err := stats.Counts(nil, from, from.Add(time.Hour)); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("nil ctx 应 ErrInvalid，实际 %v", err)
	}
	//nolint:staticcheck // 同上
	if _, err := stats.LastSeen(nil); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("LastSeen nil ctx 应 ErrInvalid，实际 %v", err)
	}
	if _, err := NewStats(nil); err == nil {
		t.Error("nil db 应报错")
	}

	// 零长度窗口：不报错，返回空统计（左闭右开自然为空）
	empty, err := stats.Counts(ctx, from, from)
	if err != nil {
		t.Fatalf("零长度窗口不应报错：%v", err)
	}
	if empty.Received != 0 {
		t.Errorf("零长度窗口应无数据，实际 %+v", empty)
	}
}
