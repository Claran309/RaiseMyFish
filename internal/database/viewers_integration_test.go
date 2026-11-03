//go:build integration

package database

// 本文件用真实 PG 测观众档案：聚合口径、门槛、注入读取、重建幂等、迁移幂等。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

// seedViewerEvent 插一条事件（in 或 out），out 用 replyTo 指回 in。
func seedViewerEvent(t *testing.T, repo *EventRepo, id, uid, name, dir, text, replyTo string, at time.Time) {
	t.Helper()
	fullID := "live:30308220:" + id
	ref := ""
	if replyTo != "" {
		ref = "live:30308220:" + replyTo
	}
	// in 事件的初始状态必须是 pending；out 事件用 done。
	status := event.StatusPending
	if event.Dir(dir) == event.DirOut {
		status = event.StatusDone
	}
	_, err := repo.Insert(context.Background(), event.Event{
		ID: fullID, Channel: event.ChLive, Dir: event.Dir(dir), Session: "30308220",
		FromID: uid, FromName: name, Text: text, ReplyTo: ref,
		At: at, Status: status,
	})
	if err != nil {
		t.Fatalf("插入事件 %s 失败：%v", id, err)
	}
}

// TestViewerProfileRebuild 3 个观众（1/2/5 句）→ 只建 2 个档案。
func TestViewerProfileRebuild(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	viewers, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	// 路人：只说 1 句 → 不建档
	seedViewerEvent(t, events, "v:a", "1001", "路人甲", "in", "路过", "", base)
	// 熟客：2 句 + 鱼回 2 次
	seedViewerEvent(t, events, "v:b1", "2002", "熟客乙", "in", "早上好", "", base.Add(time.Minute))
	seedViewerEvent(t, events, "v:b2", "2002", "熟客乙", "in", "又来了", "", base.Add(2*time.Minute))
	seedViewerEvent(t, events, "v:bo1", "", "", "out", "早啊", "v:b1", base.Add(90*time.Second))
	seedViewerEvent(t, events, "v:bo2", "", "", "out", "欢迎回来", "v:b2", base.Add(3*time.Minute))
	// 老熟人：5 句 + 鱼回 1 次
	for i := 0; i < 5; i++ {
		seedViewerEvent(t, events, fmt.Sprintf("v:c%d", i), "3003", "老熟人丙", "in",
			fmt.Sprintf("第%d句", i), "", base.Add(time.Duration(10+i)*time.Minute))
	}
	seedViewerEvent(t, events, "v:co1", "", "", "out", "好久不见", "v:c0", base.Add(11*time.Minute))

	stats, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10})
	if err != nil {
		t.Fatalf("重建失败：%v", err)
	}
	if stats.Profiles != 2 {
		t.Fatalf("建档数 = %d，期望 2（1 句的路人不建）", stats.Profiles)
	}

	// 路人没有档案
	if _, err := viewers.GetProfile(ctx, "1001"); err == nil {
		t.Error("1 句的观众不应有档案")
	}

	// 熟客字段正确
	p, err := viewers.GetProfile(ctx, "2002")
	if err != nil {
		t.Fatalf("读熟客档案失败：%v", err)
	}
	if p.Hits != 2 || p.Replies != 2 || p.Name != "熟客乙" {
		t.Errorf("熟客档案不符：%+v", p)
	}
	if len(p.RecentTexts) != 2 || len(p.RecentReplies) != 2 {
		t.Errorf("快照条数不符：%+v", p)
	}
	// 新的在前
	if p.RecentTexts[0] != "又来了" {
		t.Errorf("发言未按时间倒序：%v", p.RecentTexts)
	}

	// 老熟人：5 句
	c, err := viewers.GetProfile(ctx, "3003")
	if err != nil {
		t.Fatalf("读老熟人档案失败：%v", err)
	}
	if c.Hits != 5 || c.Replies != 1 {
		t.Errorf("老熟人档案不符：%+v", c)
	}

	// 榜单按 hits 倒序
	top, err := viewers.TopViewers(ctx, 10)
	if err != nil {
		t.Fatalf("读榜单失败：%v", err)
	}
	if len(top) != 2 || top[0].UID != "3003" {
		t.Errorf("榜单不符：%+v", top)
	}

	// 再跑一次：幂等（不重复建、计数不翻倍）
	again, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10})
	if err != nil {
		t.Fatalf("二次重建失败：%v", err)
	}
	if again.Profiles != 2 {
		t.Errorf("幂等重建建档数 = %d，期望 2", again.Profiles)
	}
	p2, _ := viewers.GetProfile(ctx, "2002")
	if p2.Hits != 2 || p2.Replies != 2 {
		t.Errorf("重建后计数被累加：%+v", p2)
	}
}

// TestViewerProfileMigrationIdempotent 迁移重复执行不报错。
func TestViewerProfileMigrationIdempotent(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("重复迁移失败（应幂等）：%v", err)
	}
	var exists bool
	if err := db.WithContext(ctx).Raw("SELECT to_regclass('viewer_profiles') IS NOT NULL").Scan(&exists).Error; err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("viewer_profiles 表不存在")
	}
}

// TestViewerActiveSince 活跃判定：窗口外的事件不算活跃。
func TestViewerActiveSince(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, _ := NewEventRepo(db)
	viewers, _ := NewViewerRepo(db)
	base := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	seedViewerEvent(t, events, "v:act", "4004", "活跃", "in", "在吗", "", base)

	active, err := viewers.ActiveSince(ctx, base.Add(-5*time.Minute))
	if err != nil || !active {
		t.Errorf("窗口内应判定活跃：%v / %v", active, err)
	}
	active, err = viewers.ActiveSince(ctx, base.Add(time.Minute))
	if err != nil || active {
		t.Errorf("窗口外应判定不活跃：%v / %v", active, err)
	}
}
