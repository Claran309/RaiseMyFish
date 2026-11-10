//go:build integration

package database

// 观众侧写的真实 PG 测试：候选选择、事务写入、记忆幂等、档案重建保留侧写。

import (
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

func summaryRecord(uid, name, text string, at time.Time, ids []string) store.MemoryRecord {
	return store.MemoryRecord{
		Record: memory.Record{
			ID: "viewer-profile:" + uid, Summary: text, Importance: 0.45, Confidence: 0.6,
			Private: false, EventIDs: ids, ValidFrom: at,
		},
		Source: "content", Kind: "viewer_profile", Title: "观众侧写 · " + name,
		FromID: uid, FromName: name, AssertedByID: uid, AssertedByName: name,
		Subject: memory.SoulSubjectViewer,
	}
}

func seedSummaryViewer(t *testing.T, dbEvents *EventRepo, uid string, base time.Time) {
	t.Helper()
	seedViewerEvent(t, dbEvents, "s:"+uid+"-1", uid, "侧写观众", "in", "第一句", "", base)
	seedViewerEvent(t, dbEvents, "s:"+uid+"-2", uid, "侧写观众", "in", "第二句", "", base.Add(time.Minute))
}

func TestViewerSummarySelectionAndSave(t *testing.T) {
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
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	seedSummaryViewer(t, events, "5005", base)
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}

	// 从未侧写 → 是候选。
	candidates, err := viewers.ActiveViewers(ctx, base.Add(-24*time.Hour), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].UID != "5005" || candidates[0].ProfileSummary != "" {
		t.Fatalf("候选不符：%+v", candidates)
	}

	at := base.Add(30 * time.Minute)
	record := summaryRecord("5005", "侧写观众", "性格：话痨；偏好：白饭", at, []string{"live:30308220:s:5005-1", "live:30308220:s:5005-2"})
	if err := viewers.SaveProfileSummary(ctx, "5005", record.Summary, at, record); err != nil {
		t.Fatalf("保存侧写失败：%v", err)
	}
	profile, err := viewers.GetProfile(ctx, "5005")
	if err != nil {
		t.Fatal(err)
	}
	if profile.ProfileSummary != record.Summary || !profile.SummaryUpdatedAt.Equal(at) {
		t.Fatalf("档案侧写未落库：%+v", profile)
	}

	// 记忆行字段完整且公开。
	memories, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := memories.ListRecords(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	var saved *store.MemoryRecord
	for i := range rows {
		if rows[i].ID == record.ID {
			saved = &rows[i]
		}
	}
	if saved == nil {
		t.Fatalf("记忆记录缺失：%+v", rows)
	}
	if saved.Kind != "viewer_profile" || saved.Source != "content" || saved.Private ||
		saved.Subject != memory.SoulSubjectViewer || saved.FromID != "5005" ||
		saved.AssertedByID != "5005" || saved.Summary != record.Summary {
		t.Fatalf("记忆行字段不符：%+v", saved)
	}
	if len(saved.EventIDs) != 2 {
		t.Fatalf("记忆来源事件不符：%+v", saved.EventIDs)
	}

	// 侧写之后没有新发言 → 不再是候选。
	candidates, err = viewers.ActiveViewers(ctx, base.Add(-24*time.Hour), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("已侧写且无新发言不应再入选：%+v", candidates)
	}

	// 重复侧写：档案覆盖、记忆行仍只有一条。
	record2 := summaryRecord("5005", "侧写观众", "性格：安静", at.Add(time.Hour), record.EventIDs)
	if err := viewers.SaveProfileSummary(ctx, "5005", record2.Summary, at.Add(time.Hour), record2); err != nil {
		t.Fatalf("二次侧写失败：%v", err)
	}
	var count int64
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM memory_records WHERE id = ?", record.ID).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("同一观众应只有一条侧写记忆：%d", count)
	}
	profile, _ = viewers.GetProfile(ctx, "5005")
	if profile.ProfileSummary != "性格：安静" {
		t.Fatalf("档案侧写未覆盖：%+v", profile)
	}

	// 档案重建必须保留侧写字段。
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	profile, _ = viewers.GetProfile(ctx, "5005")
	if profile.ProfileSummary != "性格：安静" || profile.SummaryUpdatedAt.IsZero() {
		t.Fatalf("档案重建丢失侧写：%+v", profile)
	}

	// 新发言晚于侧写时间 → 重新成为候选。
	seedViewerEvent(t, events, "s:5005-3", "5005", "侧写观众", "in", "第三句", "", at.Add(2*time.Hour))
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	candidates, err = viewers.ActiveViewers(ctx, base.Add(-24*time.Hour), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].UID != "5005" {
		t.Fatalf("侧写后新发言应重新入选：%+v", candidates)
	}
}

func TestViewerSummaryMemoryConflictRollsBack(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, _ := NewEventRepo(db)
	viewers, _ := NewViewerRepo(db)
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	seedSummaryViewer(t, events, "6006", base)
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}

	// 同 ID 的人工 fact 行占用：viewer_profile 的 upsert 必须拒绝，而不是改写它。
	err := db.WithContext(ctx).Exec(`INSERT INTO memory_records
(id,source,kind,title,summary,importance,confidence,private,valid_from)
VALUES (?,?,?,?,?,?,?,? ,clock_timestamp())`,
		"viewer-profile:6006", "content", "fact", "手工事实", "人工内容", 0.8, 0.9, false).Error
	if err != nil {
		t.Fatalf("插入冲突记忆失败：%v", err)
	}
	at := base.Add(time.Hour)
	record := summaryRecord("6006", "侧写观众", "性格：话痨", at, []string{"live:30308220:s:6006-1"})
	err = viewers.SaveProfileSummary(ctx, "6006", record.Summary, at, record)
	if err == nil || !strings.Contains(err.Error(), "保存观众侧写失败") {
		t.Fatalf("同 ID 的人工 fact 行不应被侧写覆盖：%v", err)
	}
	profile, _ := viewers.GetProfile(ctx, "6006")
	if profile.ProfileSummary != "" || !profile.SummaryUpdatedAt.IsZero() {
		t.Fatalf("事务应回滚档案侧写：%+v", profile)
	}
	var kind, summary string
	if err := db.WithContext(ctx).Raw("SELECT kind, summary FROM memory_records WHERE id = ?", record.ID).Row().Scan(&kind, &summary); err != nil {
		t.Fatal(err)
	}
	if kind != "fact" || summary != "人工内容" {
		t.Fatalf("人工记忆被改写：kind=%s summary=%q", kind, summary)
	}
}

func TestViewerSummaryMigrationReady(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("重复迁移应幂等：%v", err)
	}
	if err := Ready(ctx, db); err != nil {
		t.Fatalf("Ready 失败：%v", err)
	}
	var hasSummary, hasUpdated bool
	if err := db.WithContext(ctx).Raw(`SELECT
  EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name='viewer_profiles' AND column_name='profile_summary'),
  EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name='viewer_profiles' AND column_name='summary_updated_at')`).Row().Scan(&hasSummary, &hasUpdated); err != nil {
		t.Fatal(err)
	}
	if !hasSummary || !hasUpdated {
		t.Fatalf("0017 列缺失：profile_summary=%v summary_updated_at=%v", hasSummary, hasUpdated)
	}
	var version int64
	if err := db.WithContext(ctx).Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != 17 {
		t.Fatalf("迁移版本 = %d，期望 17", version)
	}
}

func TestViewerRecentExcludesPrivateForSummary(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, _ := NewEventRepo(db)
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	seedViewerEvent(t, events, "p:1", "7007", "私信观众", "in", "公开弹幕", "", base)
	// 私密入向与 DM 入向都不能进入侧写输入（私密只能落在 QQ/DM 通道）。
	_, err := events.Insert(ctx, eventForSummaryTest("p:2", "7007", "私密弹幕", event.ChQQ, true, base.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = events.Insert(ctx, eventForSummaryTest("p:3", "7007", "私信内容", event.ChDM, true, base.Add(2*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := events.ViewerRecent(ctx, "7007", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text != "公开弹幕" {
		t.Fatalf("侧写输入混入私密/DM：%+v", msgs)
	}
}

func eventForSummaryTest(id, uid, text string, channel event.Channel, private bool, at time.Time) event.Event {
	return event.Event{
		ID: string(channel) + ":7007:" + id, Channel: channel, Dir: event.DirIn, Session: "7007",
		FromID: uid, FromName: "私信观众", Text: text, Private: private, At: at,
		Status: event.StatusPending,
	}
}
