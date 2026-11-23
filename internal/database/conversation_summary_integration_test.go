//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

// TestConversationSummaryJobsEnqueueAndClaim C5：轮数阈值/空闲触发入队，租约领取。
func TestConversationSummaryJobsEnqueueAndClaim(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	conversations, err := NewConversationRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := NewConversationSummaryJobsRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// 会话 A：新增轮数达到阈值，应立即入队。
	if err := conversations.Touch(ctx, store.ConversationState{
		ConversationID: "dm:1:2", Channel: "dm", TurnCount: 10, LastEventID: "e10",
		LastTurnAt: now.Add(-10 * time.Second), ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// 会话 B：轮数不足但空闲超过阈值，也应入队。
	if err := conversations.Touch(ctx, store.ConversationState{
		ConversationID: "dm:1:3", Channel: "dm", TurnCount: 2, LastEventID: "e2",
		LastTurnAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// 会话 C：轮数不足且刚说过话，不应入队。
	if err := conversations.Touch(ctx, store.ConversationState{
		ConversationID: "dm:1:4", Channel: "dm", TurnCount: 2, LastEventID: "e2",
		LastTurnAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	n, err := jobs.EnqueueSummaries(ctx, now, nil, 8, 5*time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("入队条数=%d，期望 2", n)
	}
	// 幂等：同一目标事件重复入队不再新增。
	if n, err = jobs.EnqueueSummaries(ctx, now, nil, 8, 5*time.Minute, 100); err != nil || n != 0 {
		t.Fatalf("重复入队=%d err=%v", n, err)
	}

	claimed, err := jobs.ClaimSummaries(ctx, "owner-1", time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("领取条数=%d，期望 2", len(claimed))
	}
	for _, job := range claimed {
		if job.ConversationID == "dm:1:4" {
			t.Fatal("未达阈值的会话被领取")
		}
		if job.Status != "running" || job.Attempts != 1 {
			t.Fatalf("任务状态错误：%+v", job)
		}
	}
	// 已被领取的任务不会被第二个 worker 再领。
	again, err := jobs.ClaimSummaries(ctx, "owner-2", time.Minute, 10)
	if err != nil || len(again) != 0 {
		t.Fatalf("重复领取=%d err=%v", len(again), err)
	}
}

// TestConversationSummaryCompleteWritesStateAndIsIdempotent 完成写回摘要并推进覆盖位置。
func TestConversationSummaryCompleteWritesStateAndIsIdempotent(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	conversations, _ := NewConversationRepo(db)
	jobs, _ := NewConversationSummaryJobsRepo(db)
	now := time.Now().UTC()
	if err := conversations.Touch(ctx, store.ConversationState{
		ConversationID: "dm:9:9", Channel: "dm", Summary: "旧摘要", TurnCount: 12, LastEventID: "e12",
		LastTurnAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := jobs.EnqueueSummaries(ctx, now, nil, 8, 0, 10); err != nil || n != 1 {
		t.Fatalf("入队=%d err=%v", n, err)
	}
	claimed, err := jobs.ClaimSummaries(ctx, "owner-1", time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("领取失败：%v %v", claimed, err)
	}
	job := claimed[0]
	if err := jobs.CompleteSummary(ctx, job, "新摘要：在问鱼的价格", ""); err != nil {
		t.Fatal(err)
	}
	state, ok, err := conversations.LoadState(ctx, "dm:9:9", now)
	if err != nil || !ok || state.Summary != "新摘要：在问鱼的价格" {
		t.Fatalf("摘要未写回：%+v %v %v", state, ok, err)
	}
	// 覆盖位置推进后，同一批轮次不再入队。
	if n, err := jobs.EnqueueSummaries(ctx, now, nil, 8, 0, 10); err != nil || n != 0 {
		t.Fatalf("重复入队=%d err=%v", n, err)
	}
	// 旧租约的完成请求被栅栏拒绝。
	if err := jobs.CompleteSummary(ctx, job, "覆盖摘要", ""); err == nil {
		t.Fatal("过期租约仍能写摘要")
	}
}

// TestConversationSummaryFailAndRenew 失败回队、续租与失租冲突。
func TestConversationSummaryFailAndRenew(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	conversations, _ := NewConversationRepo(db)
	jobs, _ := NewConversationSummaryJobsRepo(db)
	now := time.Now().UTC()
	if err := conversations.Touch(ctx, store.ConversationState{
		ConversationID: "dm:5:6", Channel: "dm", TurnCount: 9, LastEventID: "e9",
		LastTurnAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.EnqueueSummaries(ctx, now, nil, 8, 0, 10); err != nil {
		t.Fatal(err)
	}
	claimed, err := jobs.ClaimSummaries(ctx, "owner-1", time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("领取失败：%v %v", claimed, err)
	}
	job := claimed[0]
	renewed, err := jobs.RenewSummary(ctx, job, time.Minute)
	if err != nil || renewed.LeaseOwner != "owner-1" {
		t.Fatalf("续租失败：%+v %v", renewed, err)
	}
	if err := jobs.FailSummary(ctx, job, "模型超时", true, 3); err != nil {
		t.Fatal(err)
	}
	// 回队后租约已释放，旧持有者不能再续租。
	if _, err := jobs.RenewSummary(ctx, job, time.Minute); err == nil {
		t.Fatal("释放后的租约仍能续租")
	}
	reclaimed, err := jobs.ClaimSummaries(ctx, "owner-2", time.Minute, 10)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Attempts != 2 {
		t.Fatalf("失败后未回队：%+v %v", reclaimed, err)
	}
	// 超过上限后置 failed，不再被领取。
	if err := jobs.FailSummary(ctx, reclaimed[0], "再次失败", true, 2); err != nil {
		t.Fatal(err)
	}
	if got, err := jobs.ClaimSummaries(ctx, "owner-3", time.Minute, 10); err != nil || len(got) != 0 {
		t.Fatalf("超限任务仍被领取：%+v %v", got, err)
	}
}
