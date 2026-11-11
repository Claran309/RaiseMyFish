//go:build integration

package database

// 观众侧写的真实 PG 测试：候选选择、事务写入、来源 CAS、撤回、补偿状态与投影任务。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

func summaryRecord(uid, name, text string, at time.Time, ids []string) store.MemoryRecord {
	// R05：派生侧写 subject=观众、derived_by=模型，asserted_by 留空。
	return store.MemoryRecord{
		Record: memory.Record{
			ID: "viewer-profile:" + uid, Summary: text, Importance: 0.45, Confidence: 0.6,
			Private: false, EventIDs: ids, ValidFrom: at,
		},
		Source: "content", Kind: "viewer_profile", Title: "观众侧写 · " + name,
		FromID: uid, FromName: name,
		Subject:   memory.SoulSubjectViewer,
		DerivedBy: "model:" + store.ViewerSummaryGenerator,
		Evidence: []store.MemoryEvidence{
			{Field: "性格", Type: store.ViewerSummaryBasisSelf, Text: "第一句"},
		},
	}
}

// activeSummaryViewers 用测试口径包装新查询结构：新消息门槛 1、回填足量。
func activeSummaryViewers(ctx context.Context, viewers *ViewerRepo, since, now time.Time, minHits, limit int) ([]store.ViewerProfile, error) {
	return viewers.ActiveViewers(ctx, store.ViewerSummaryQuery{
		Since: since, Now: now, MinHits: minHits, MinNewMessages: 1,
		Limit: limit, BackfillLimit: limit,
	})
}

func seedSummaryViewer(t *testing.T, dbEvents *EventRepo, uid string, base time.Time) {
	t.Helper()
	seedViewerEvent(t, dbEvents, "s:"+uid+"-1", uid, "侧写观众", "in", "第一句", "", base)
	seedViewerEvent(t, dbEvents, "s:"+uid+"-2", uid, "侧写观众", "in", "第二句", "", base.Add(time.Minute))
}

// summaryWriteFrom 用与侧写任务相同的方式（ViewerRecent 快照）构造落库请求，
// 保证来源指纹就是数据库里的真实事件版本。
func summaryWriteFrom(t *testing.T, ctx context.Context, repo *EventRepo, uid, name, text string, at time.Time) store.ViewerSummaryWrite {
	t.Helper()
	msgs, err := repo.ViewerRecent(ctx, uid, 20)
	if err != nil {
		t.Fatalf("读取观众最近发言失败：%v", err)
	}
	sources := make([]store.ViewerSummarySource, 0, len(msgs))
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		sources = append(sources, store.ViewerSummarySource{ID: msg.ID, Text: msg.Text, At: msg.At})
		ids = append(ids, msg.ID)
	}
	record := summaryRecord(uid, name, text, at, ids)
	return store.ViewerSummaryWrite{
		UID: uid, Summary: text, At: at, Record: record,
		SourceHash: store.ViewerSummarySourceHash(uid, store.ViewerSummaryGenerator, sources),
		Sources:    sources, Generator: store.ViewerSummaryGenerator,
		Keywords: []string{name},
	}
}

func summaryJob(t *testing.T, db *gorm.DB, ctx context.Context, uid string) (action string, attempts int) {
	t.Helper()
	err := db.WithContext(ctx).Raw("SELECT action, attempts FROM viewer_summary_jobs WHERE uid = ?", uid).Row().Scan(&action, &attempts)
	if err != nil {
		t.Fatalf("读取投影任务失败：%v", err)
	}
	return action, attempts
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
	candidates, err := activeSummaryViewers(ctx, viewers, base.Add(-24*time.Hour), base.Add(30*time.Minute), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].UID != "5005" || candidates[0].ProfileSummary != "" {
		t.Fatalf("候选不符：%+v", candidates)
	}

	at := base.Add(30 * time.Minute)
	write := summaryWriteFrom(t, ctx, events, "5005", "侧写观众", "性格：话痨；偏好：白饭", at)
	if err := viewers.SaveProfileSummary(ctx, write); err != nil {
		t.Fatalf("保存侧写失败：%v", err)
	}
	profile, err := viewers.GetProfile(ctx, "5005")
	if err != nil {
		t.Fatal(err)
	}
	if profile.ProfileSummary != write.Summary || !profile.SummaryUpdatedAt.Equal(at) {
		t.Fatalf("档案侧写未落库：%+v", profile)
	}
	if profile.SummarySourceHash != write.SourceHash || profile.SummaryGenerator != store.ViewerSummaryGenerator ||
		len(profile.SummarySourceIDs) != 2 || len(profile.SummaryKeywords) != 1 {
		t.Fatalf("来源指纹/生成器/关键词未落库：%+v", profile)
	}
	if !profile.SummaryLastSuccessAt.Equal(at) || !profile.SummaryAttemptedAt.Equal(at) ||
		profile.SummaryFailures != 0 || !profile.SummaryNextRetryAt.IsZero() {
		t.Fatalf("成功状态字段不符：%+v", profile)
	}
	if action, attempts := summaryJob(t, db, ctx, "5005"); action != store.ViewerSummaryActionPublish || attempts != 0 {
		t.Fatalf("投影任务未登记：action=%s attempts=%d", action, attempts)
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
		if rows[i].ID == write.Record.ID {
			saved = &rows[i]
		}
	}
	if saved == nil {
		t.Fatalf("记忆记录缺失：%+v", rows)
	}
	if saved.Kind != "viewer_profile" || saved.Source != "content" || saved.Private ||
		saved.Subject != memory.SoulSubjectViewer || saved.FromID != "5005" ||
		saved.AssertedByID != "" || saved.AssertedByName != "" ||
		saved.DerivedBy != "model:"+store.ViewerSummaryGenerator || saved.Summary != write.Summary {
		t.Fatalf("记忆行字段不符：%+v", saved)
	}
	if len(saved.EventIDs) != 2 {
		t.Fatalf("记忆来源事件不符：%+v", saved.EventIDs)
	}
	if len(saved.Evidence) != 1 || saved.Evidence[0].Field != "性格" || saved.Evidence[0].Type != store.ViewerSummaryBasisSelf {
		t.Fatalf("记忆证据未落库：%+v", saved.Evidence)
	}

	// 侧写之后没有新发言 → 不再是候选。
	candidates, err = activeSummaryViewers(ctx, viewers, base.Add(-24*time.Hour), at.Add(time.Hour), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("已侧写且无新发言不应再入选：%+v", candidates)
	}

	// 重复侧写：档案覆盖、记忆行仍只有一条、投影任务不重复。
	write2 := summaryWriteFrom(t, ctx, events, "5005", "侧写观众", "性格：安静", at.Add(time.Hour))
	write2.Keywords = []string{"侧写观众", "安静"}
	if err := viewers.SaveProfileSummary(ctx, write2); err != nil {
		t.Fatalf("二次侧写失败：%v", err)
	}
	var count int64
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM memory_records WHERE id = ?", write.Record.ID).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("同一观众应只有一条侧写记忆：%d", count)
	}
	var jobCount int64
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM viewer_summary_jobs WHERE uid = ?", "5005").Scan(&jobCount).Error; err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("同一观众应只有一条投影任务：%d", jobCount)
	}
	profile, _ = viewers.GetProfile(ctx, "5005")
	if profile.ProfileSummary != "性格：安静" || len(profile.SummaryKeywords) != 2 {
		t.Fatalf("档案侧写未覆盖：%+v", profile)
	}

	// 档案重建必须保留侧写字段。
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	profile, _ = viewers.GetProfile(ctx, "5005")
	if profile.ProfileSummary != "性格：安静" || profile.SummaryUpdatedAt.IsZero() ||
		profile.SummarySourceHash != write2.SourceHash {
		t.Fatalf("档案重建丢失侧写：%+v", profile)
	}

	// 新发言晚于侧写时间 → 重新成为候选。
	seedViewerEvent(t, events, "s:5005-3", "5005", "侧写观众", "in", "第三句", "", at.Add(2*time.Hour))
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	candidates, err = activeSummaryViewers(ctx, viewers, base.Add(-24*time.Hour), at.Add(3*time.Hour), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].UID != "5005" {
		t.Fatalf("侧写后新发言应重新入选：%+v", candidates)
	}
}

// TestViewerSummarySourceCAS 保存时必须重算来源指纹：删除/改密/改正文都拒绝写入。
func TestViewerSummarySourceCAS(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, _ := NewEventRepo(db)
	viewers, _ := NewViewerRepo(db)
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	at := base.Add(30 * time.Minute)

	cases := []struct {
		name string
		uid  string
		sql  string
	}{
		{"来源正文被改", "5101", "UPDATE events SET text = '改过的内容' WHERE id = ?"},
		{"来源被删除", "5102", "DELETE FROM events WHERE id = ?"},
		{"来源被改密", "5103", "UPDATE events SET private = TRUE WHERE id = ?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedSummaryViewer(t, events, tc.uid, base)
			if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
				t.Fatal(err)
			}
			write := summaryWriteFrom(t, ctx, events, tc.uid, "侧写观众", "性格：话痨", at)
			if err := viewers.SaveProfileSummary(ctx, write); err != nil {
				t.Fatalf("基线侧写失败：%v", err)
			}
			target := "live:30308220:s:" + tc.uid + "-1"
			if err := db.WithContext(ctx).Exec(tc.sql, target).Error; err != nil {
				t.Fatalf("破坏来源失败：%v", err)
			}
			err := viewers.SaveProfileSummary(ctx, write)
			if !errors.Is(err, store.ErrViewerSourceChanged) {
				t.Fatalf("来源已变应拒绝保存：%v", err)
			}
			profile, _ := viewers.GetProfile(ctx, tc.uid)
			if profile.ProfileSummary != write.Summary {
				t.Fatalf("拒绝保存不得覆盖旧侧写：%+v", profile)
			}
		})
	}
}

// TestViewerSummaryRevocation 撤回/改密后派生内容立即不可见：档案清空、记忆归档、登记 purge。
func TestViewerSummaryRevocation(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, _ := NewEventRepo(db)
	viewers, _ := NewViewerRepo(db)
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	// 3 条公开弹幕：改密 1 条后仍满足建档门槛，档案存活，只有侧写被撤回。
	seedSummaryViewer(t, events, "5201", base)
	seedViewerEvent(t, events, "s:5201-3", "5201", "侧写观众", "in", "第三句", "", base.Add(2*time.Minute))
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	at := base.Add(30 * time.Minute)
	if err := viewers.SaveProfileSummary(ctx, summaryWriteFrom(t, ctx, events, "5201", "侧写观众", "性格：话痨", at)); err != nil {
		t.Fatal(err)
	}

	// 来源被改密：下一轮撤回必须先让派生内容不可见。
	if err := db.WithContext(ctx).Exec("UPDATE events SET private = TRUE WHERE id = ?", "live:30308220:s:5201-1").Error; err != nil {
		t.Fatal(err)
	}
	revoked, err := viewers.RevokeInvalidSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0] != "5201" {
		t.Fatalf("撤回名单不符：%+v", revoked)
	}
	profile, _ := viewers.GetProfile(ctx, "5201")
	if profile.ProfileSummary != "" || !profile.SummaryUpdatedAt.IsZero() ||
		profile.SummarySourceHash != "" || len(profile.SummarySourceIDs) != 0 ||
		!profile.SummaryAttemptedAt.IsZero() || profile.SummaryFailures != 0 {
		t.Fatalf("撤回后侧写状态未清空：%+v", profile)
	}
	var archived bool
	if err := db.WithContext(ctx).Raw("SELECT archived FROM memory_records WHERE id = ?", "viewer-profile:5201").Scan(&archived).Error; err != nil {
		t.Fatalf("读取记忆归档状态失败：%v", err)
	}
	if !archived {
		t.Fatal("撤回后记忆行必须归档")
	}
	if action, _ := summaryJob(t, db, ctx, "5201"); action != store.ViewerSummaryActionPurge {
		t.Fatalf("撤回后应登记 purge 任务：action=%s", action)
	}
	// 已可重新侧写（等新发言或候选窗口内重建来源）。
	candidates, err := activeSummaryViewers(ctx, viewers, base.Add(-24*time.Hour), at.Add(time.Hour), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].UID != "5201" {
		t.Fatalf("撤回后应重新成为候选：%+v", candidates)
	}
	// 重复 sweep 不重复撤回。
	if revoked, err := viewers.RevokeInvalidSummaries(ctx); err != nil || len(revoked) != 0 {
		t.Fatalf("重复撤回不应再次命中：%+v err=%v", revoked, err)
	}
}

// TestViewerSummaryRevokeOnRebuild 档案重建同时撤回来源失效的侧写，并返回 UID 供知识清理。
func TestViewerSummaryRevokeOnRebuild(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, _ := NewEventRepo(db)
	viewers, _ := NewViewerRepo(db)
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	seedSummaryViewer(t, events, "5301", base)
	seedViewerEvent(t, events, "s:5301-3", "5301", "侧写观众", "in", "第三句", "", base.Add(2*time.Minute))
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	at := base.Add(30 * time.Minute)
	if err := viewers.SaveProfileSummary(ctx, summaryWriteFrom(t, ctx, events, "5301", "侧写观众", "性格：话痨", at)); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Exec("DELETE FROM events WHERE id = ?", "live:30308220:s:5301-2").Error; err != nil {
		t.Fatal(err)
	}
	// 删除全部来源后档案也会消失（hits 不足）；这里只删 1 条，档案存活。
	stats, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Revoked) != 1 || stats.Revoked[0] != "5301" {
		t.Fatalf("重建未撤回失效侧写：%+v", stats)
	}
	profile, _ := viewers.GetProfile(ctx, "5301")
	if profile.ProfileSummary != "" {
		t.Fatalf("重建后失效侧写仍可见：%+v", profile)
	}
	if action, _ := summaryJob(t, db, ctx, "5301"); action != store.ViewerSummaryActionPurge {
		t.Fatalf("重建撤回后应登记 purge：action=%s", action)
	}

	// 迁移前生成、没有来源指纹的旧侧写按 fail-closed 撤回。
	seedSummaryViewer(t, events, "5302", base)
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Exec(`UPDATE viewer_profiles SET profile_summary='旧侧写', summary_updated_at=clock_timestamp()
WHERE uid = ?`, "5302").Error; err != nil {
		t.Fatal(err)
	}
	stats, err = viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Revoked) != 1 || stats.Revoked[0] != "5302" {
		t.Fatalf("无来源指纹的旧侧写应撤回：%+v", stats)
	}
}

// TestViewerSummaryDeferAndJobs 失败补偿状态与投影任务：好版本保留、退避有上限、任务可完成/重试。
func TestViewerSummaryDeferAndJobs(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, _ := NewEventRepo(db)
	viewers, _ := NewViewerRepo(db)
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	seedSummaryViewer(t, events, "5401", base)
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	at := base.Add(30 * time.Minute)
	if err := viewers.SaveProfileSummary(ctx, summaryWriteFrom(t, ctx, events, "5401", "侧写观众", "性格：话痨", at)); err != nil {
		t.Fatal(err)
	}

	// 保留旧版本：只推进尝试/失败/重试，不改内容与更新时间。
	retryAt := at.Add(10 * time.Minute)
	if err := viewers.DeferProfileSummary(ctx, store.ViewerSummaryDefer{UID: "5401", At: at.Add(time.Minute), Failures: 1, NextRetryAt: retryAt}); err != nil {
		t.Fatal(err)
	}
	profile, _ := viewers.GetProfile(ctx, "5401")
	if profile.ProfileSummary != "性格：话痨" || !profile.SummaryUpdatedAt.Equal(at) {
		t.Fatalf("保留旧版本被覆盖：%+v", profile)
	}
	if profile.SummaryFailures != 1 || !profile.SummaryNextRetryAt.Equal(retryAt) || !profile.SummaryAttemptedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("补偿状态不符：%+v", profile)
	}
	// 未到重试时间不入选；到期后重新入选。
	candidates, err := activeSummaryViewers(ctx, viewers, base.Add(-24*time.Hour), at.Add(5*time.Minute), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("退避未到期不应入选：%+v", candidates)
	}
	candidates, err = activeSummaryViewers(ctx, viewers, base.Add(-24*time.Hour), retryAt, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("退避到期应重新入选：%+v", candidates)
	}
	// 达到上限：不安排重试，等新发言。
	if err := viewers.DeferProfileSummary(ctx, store.ViewerSummaryDefer{UID: "5401", At: at.Add(20 * time.Minute), Failures: 5}); err != nil {
		t.Fatal(err)
	}
	candidates, err = activeSummaryViewers(ctx, viewers, base.Add(-24*time.Hour), at.Add(21*time.Minute), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("达到上限后不应自动重试：%+v", candidates)
	}
	seedViewerEvent(t, events, "s:5401-3", "5401", "侧写观众", "in", "第三句", "", at.Add(2*time.Hour))
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	candidates, err = activeSummaryViewers(ctx, viewers, base.Add(-24*time.Hour), at.Add(3*time.Hour), 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("新发言后应重新入选：%+v", candidates)
	}

	// 投影任务：到期才可见，失败退避，完成后删除；purge 优先。
	now := time.Now().UTC()
	jobs, err := viewers.PendingSummaryJobs(ctx, now.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].UID != "5401" || jobs[0].Action != store.ViewerSummaryActionPublish {
		t.Fatalf("保存后应有 publish 任务：%+v", jobs)
	}
	if err := viewers.FailSummaryJob(ctx, "5401", store.ViewerSummaryActionPublish, 1, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	jobs, err = viewers.PendingSummaryJobs(ctx, now.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("失败退避中的任务不应到期：%+v", jobs)
	}
	jobs, err = viewers.PendingSummaryJobs(ctx, now.Add(2*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Attempts != 1 {
		t.Fatalf("到期后应带尝试次数重新可见：%+v", jobs)
	}
	if err := viewers.FinishSummaryJob(ctx, "5401", store.ViewerSummaryActionPublish); err != nil {
		t.Fatal(err)
	}
	jobs, err = viewers.PendingSummaryJobs(ctx, now.Add(2*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("完成后任务应删除：%+v", jobs)
	}
}

// TestViewerSummaryLayersAndExpiry 记住与过期：分层列落库、记忆证据同步、稳定层也能撤回。
func TestViewerSummaryLayersAndExpiry(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, _ := NewEventRepo(db)
	viewers, _ := NewViewerRepo(db)
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	seedSummaryViewer(t, events, "5501", base)
	seedSummaryViewer(t, events, "5502", base)
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	at := base.Add(30 * time.Minute)
	until := at.Add(7 * 24 * time.Hour)
	stable := "性格：话痨（自述）"
	recent := "近期观察，仅参考：偏好：猫猫（观察）（有效期至 2026-11-17）"
	write := summaryWriteFrom(t, ctx, events, "5501", "侧写观众", stable+"；"+recent, at)
	write.Stable = stable
	write.Recent = recent
	write.RecentUntil = until
	write.Fields = []store.ViewerSummaryField{
		{Name: "性格", Value: "话痨", Basis: store.ViewerSummaryBasisSelf, Evidence: "我是话痨", Layer: store.ViewerSummaryLayerStable, UpdatedAt: at},
		{Name: "偏好", Value: "猫猫", Basis: store.ViewerSummaryBasisObserved, Evidence: "猫猫好可爱", Layer: store.ViewerSummaryLayerRecent, UpdatedAt: at, ExpiresAt: until},
	}
	write.Record.Evidence = []store.MemoryEvidence{
		{Field: "性格", Type: store.ViewerSummaryBasisSelf, Text: "我是话痨"},
		{Field: "偏好", Type: store.ViewerSummaryBasisObserved, Text: "猫猫好可爱"},
	}
	if err := viewers.SaveProfileSummary(ctx, write); err != nil {
		t.Fatalf("保存分层侧写失败：%v", err)
	}
	profile, _ := viewers.GetProfile(ctx, "5501")
	if profile.SummaryStable != stable || profile.SummaryRecent != recent ||
		!profile.SummaryRecentUntil.Equal(until) || len(profile.SummaryFields) != 2 {
		t.Fatalf("分层列未落库：%+v", profile)
	}

	// 未到期不清。
	if expired, err := viewers.ExpireProfileSummaryRecent(ctx, at.Add(24*time.Hour)); err != nil || len(expired) != 0 {
		t.Fatalf("未到期不应清理：%+v err=%v", expired, err)
	}
	expired, err := viewers.ExpireProfileSummaryRecent(ctx, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0] != "5501" {
		t.Fatalf("到期名单不符：%+v", expired)
	}
	profile, _ = viewers.GetProfile(ctx, "5501")
	if profile.ProfileSummary != stable || profile.SummaryStable != stable ||
		profile.SummaryRecent != "" || !profile.SummaryRecentUntil.IsZero() || len(profile.SummaryFields) != 1 {
		t.Fatalf("过期后应退回稳定层：%+v", profile)
	}
	var summary, evidence string
	if err := db.WithContext(ctx).Raw("SELECT summary, evidence::text FROM memory_records WHERE id = ?", "viewer-profile:5501").
		Row().Scan(&summary, &evidence); err != nil {
		t.Fatal(err)
	}
	if summary != stable || strings.Contains(evidence, "偏好") || !strings.Contains(evidence, "性格") {
		t.Fatalf("过期后记忆与证据未同步：summary=%q evidence=%q", summary, evidence)
	}
	if action, _ := summaryJob(t, db, ctx, "5501"); action != store.ViewerSummaryActionPublish {
		t.Fatalf("过期后应补投影 publish：action=%s", action)
	}

	// 只有近期层：过期后清空档案、归档记忆并登记 purge。
	write2 := summaryWriteFrom(t, ctx, events, "5502", "侧写观众", "近期观察，仅参考：最近话题：新番（推断）", at)
	write2.Recent = "近期观察，仅参考：最近话题：新番（推断）（有效期至 2026-11-17）"
	write2.RecentUntil = until
	write2.Fields = []store.ViewerSummaryField{
		{Name: "最近话题", Value: "新番", Basis: store.ViewerSummaryBasisInferred, Layer: store.ViewerSummaryLayerRecent, UpdatedAt: at, ExpiresAt: until},
	}
	if err := viewers.SaveProfileSummary(ctx, write2); err != nil {
		t.Fatal(err)
	}
	expired, err = viewers.ExpireProfileSummaryRecent(ctx, until)
	if err != nil || len(expired) != 1 || expired[0] != "5502" {
		t.Fatalf("第二批到期名单不符：%+v err=%v", expired, err)
	}
	profile, _ = viewers.GetProfile(ctx, "5502")
	if profile.ProfileSummary != "" || !profile.SummaryUpdatedAt.IsZero() || len(profile.SummaryFields) != 0 {
		t.Fatalf("无稳定层的过期应清空档案：%+v", profile)
	}
	var archived bool
	if err := db.WithContext(ctx).Raw("SELECT archived FROM memory_records WHERE id = ?", "viewer-profile:5502").Scan(&archived).Error; err != nil {
		t.Fatal(err)
	}
	if !archived {
		t.Fatal("无稳定层的过期应归档记忆")
	}
	if action, _ := summaryJob(t, db, ctx, "5502"); action != store.ViewerSummaryActionPurge {
		t.Fatalf("无稳定层的过期应登记 purge：action=%s", action)
	}
}

// TestViewerSummarySelectionRules R09：首次回填按熟人小批、更新按新消息量、日预算与前台忙碌可查。
func TestViewerSummarySelectionRules(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, _ := NewEventRepo(db)
	viewers, _ := NewViewerRepo(db)
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	counts := map[string]int{"6101": 2, "6102": 4, "6103": 6}
	for _, uid := range []string{"6101", "6102", "6103"} {
		for n := 1; n <= counts[uid]; n++ {
			seedViewerEvent(t, events, "s:"+uid+"-"+string(rune('a'+n)), uid, "观众"+uid, "in", "发言", "", base.Add(time.Duration(n)*time.Minute))
		}
	}
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	query := func(now time.Time, backfill int) []store.ViewerProfile {
		t.Helper()
		candidates, err := viewers.ActiveViewers(ctx, store.ViewerSummaryQuery{
			Since: now.Add(-24 * time.Hour), Now: now, MinHits: 2,
			MinNewMessages: 3, Limit: 10, BackfillLimit: backfill,
		})
		if err != nil {
			t.Fatal(err)
		}
		return candidates
	}
	if got := query(base.Add(time.Hour), 0); len(got) != 0 {
		t.Fatalf("回填关闭时不应有候选：%+v", got)
	}
	// 首次回填：hits 最高的 6103 先试。
	got := query(base.Add(time.Hour), 1)
	if len(got) != 1 || got[0].UID != "6103" {
		t.Fatalf("首次回填应先进熟人：%+v", got)
	}
	got = query(base.Add(time.Hour), 3)
	if len(got) != 3 || got[0].UID != "6103" || got[1].UID != "6102" || got[2].UID != "6101" {
		t.Fatalf("回填顺序应按 hits 倒序：%+v", got)
	}

	// 6103 已有侧写：无新消息时不再回填；新增 2 条（不足 3）仍不入选，第 3 条才入选。
	at := base.Add(90 * time.Minute)
	if err := viewers.SaveProfileSummary(ctx, summaryWriteFrom(t, ctx, events, "6103", "观众6103", "性格：话痨", at)); err != nil {
		t.Fatal(err)
	}
	got = query(at.Add(10*time.Minute), 3)
	if len(got) != 2 || got[0].UID != "6102" || got[1].UID != "6101" {
		t.Fatalf("已侧写的观众不应占回填名额：%+v", got)
	}
	for i := 0; i < 2; i++ {
		seedViewerEvent(t, events, "s:6103-new-"+string(rune('a'+i)), "6103", "观众6103", "in", "新发言", "", at.Add(time.Duration(i+1)*time.Minute))
	}
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	if got := query(at.Add(10*time.Minute), 0); len(got) != 0 {
		t.Fatalf("新消息不足门槛不应重写：%+v", got)
	}
	seedViewerEvent(t, events, "s:6103-new-c", "6103", "观众6103", "in", "第三条", "", at.Add(3*time.Minute))
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	got = query(at.Add(10*time.Minute), 0)
	if len(got) != 1 || got[0].UID != "6103" {
		t.Fatalf("新消息达到门槛应重写：%+v", got)
	}

	// 日预算按观众数统计；前台忙碌只看回看窗口内进入 replying 的事件。
	attempts, err := viewers.SummaryAttemptsSince(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("日预算尝试人数不符：%d", attempts)
	}
	if attempts, _ := viewers.SummaryAttemptsSince(ctx, base.Add(24*time.Hour)); attempts != 0 {
		t.Fatalf("窗口外不应计入：%d", attempts)
	}
	if busy, err := viewers.ForegroundBusy(ctx, base); err != nil || busy {
		t.Fatalf("初始不应忙碌：busy=%v err=%v", busy, err)
	}
	if err := db.WithContext(ctx).Exec("UPDATE events SET status = ?, status_changed_at = ? WHERE id = ?",
		event.StatusReplying, base.Add(time.Minute), "live:30308220:s:6101-b").Error; err != nil {
		t.Fatal(err)
	}
	if busy, err := viewers.ForegroundBusy(ctx, base); err != nil || !busy {
		t.Fatalf("窗口内应判定忙碌：busy=%v err=%v", busy, err)
	}
	if busy, _ := viewers.ForegroundBusy(ctx, base.Add(2*time.Minute)); busy {
		t.Fatal("窗口外不应判定忙碌")
	}
}

// TestViewerSummaryMemoryConflictRollsBack 同 ID 的人工 fact 行：侧写 upsert 必须拒绝并回滚。
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
	write := summaryWriteFrom(t, ctx, events, "6006", "侧写观众", "性格：话痨", at)
	err = viewers.SaveProfileSummary(ctx, write)
	if err == nil || !strings.Contains(err.Error(), "保存观众侧写失败") {
		t.Fatalf("同 ID 的人工 fact 行不应被侧写覆盖：%v", err)
	}
	profile, _ := viewers.GetProfile(ctx, "6006")
	if profile.ProfileSummary != "" || !profile.SummaryUpdatedAt.IsZero() {
		t.Fatalf("事务应回滚档案侧写：%+v", profile)
	}
	var kind, summary string
	if err := db.WithContext(ctx).Raw("SELECT kind, summary FROM memory_records WHERE id = ?", write.Record.ID).Row().Scan(&kind, &summary); err != nil {
		t.Fatal(err)
	}
	if kind != "fact" || summary != "人工内容" {
		t.Fatalf("人工记忆被改写：kind=%s summary=%q", kind, summary)
	}
	var jobCount int64
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM viewer_summary_jobs WHERE uid = ?", "6006").Scan(&jobCount).Error; err != nil {
		t.Fatal(err)
	}
	if jobCount != 0 {
		t.Fatalf("失败事务不应留下投影任务：%d", jobCount)
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
	var missing int
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name='viewer_profiles'
  AND column_name IN ('profile_summary_source_hash','profile_summary_source_ids','profile_summary_generator',
    'profile_summary_keywords','profile_summary_last_success_at','profile_summary_fallback_at',
    'profile_summary_failures','profile_summary_next_retry_at','summary_attempted_at')`).Scan(&missing).Error; err != nil {
		t.Fatal(err)
	}
	if missing != 9 {
		t.Fatalf("0019 侧写状态列缺失：%d/9", missing)
	}
	if err := db.WithContext(ctx).Exec("SELECT uid,action,attempts,next_retry_at,created_at,updated_at FROM viewer_summary_jobs LIMIT 0").Error; err != nil {
		t.Fatalf("投影任务表不完整：%v", err)
	}
	if err := db.WithContext(ctx).Exec("SELECT profile_summary_stable,profile_summary_recent,profile_summary_recent_until,profile_summary_fields FROM viewer_profiles LIMIT 0").Error; err != nil {
		t.Fatalf("0020 侧写分层列缺失：%v", err)
	}
	if err := db.WithContext(ctx).Exec("SELECT derived_by,evidence FROM memory_records LIMIT 0").Error; err != nil {
		t.Fatalf("0020 记忆派生列缺失：%v", err)
	}
	var hasIndex bool
	if err := db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM pg_indexes
WHERE schemaname = current_schema() AND indexname = 'idx_events_public_from_at')`).Scan(&hasIndex).Error; err != nil {
		t.Fatal(err)
	}
	if !hasIndex {
		t.Fatal("0020 缺少按观众取新消息的部分索引")
	}
	var version int64
	if err := db.WithContext(ctx).Raw("SELECT MAX(version) FROM schema_migrations").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != 23 {
		t.Fatalf("迁移版本 = %d，期望 23", version)
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

// TestViewerSummaryPanelOps 面板计数、单观众重建与撤回：只动目标观众，撤回立即不可见。
func TestViewerSummaryPanelOps(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, _ := NewEventRepo(db)
	viewers, _ := NewViewerRepo(db)
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	seedSummaryViewer(t, events, "6101", base)
	seedSummaryViewer(t, events, "6102", base)
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	at := base.Add(30 * time.Minute)
	if err := viewers.SaveProfileSummary(ctx, summaryWriteFrom(t, ctx, events, "6101", "侧写观众", "性格：话痨", at)); err != nil {
		t.Fatal(err)
	}
	// 退避时间锚定真实墙钟：RequeueSummary 用 clock_timestamp() 写入，
	// 固定历史时刻会让「已重建」被查询 Now 误过滤。
	wall := time.Now().UTC()
	fallback := summaryWriteFrom(t, ctx, events, "6102", "侧写观众", "（模型未生成侧写，仅统计）", at.Add(time.Minute))
	fallback.Fallback = true
	fallback.Failures = 1
	fallback.NextRetryAt = wall.Add(2 * time.Hour)
	fallback.Record.Confidence = 0.4
	if err := viewers.SaveProfileSummary(ctx, fallback); err != nil {
		t.Fatal(err)
	}

	panel, err := viewers.SummaryPanel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if panel.Summarized != 2 || panel.Failed != 1 || panel.Fallback != 1 || panel.Sources != 4 {
		t.Fatalf("面板计数不符：%+v", panel)
	}
	if !panel.LastSummaryAt.Equal(at) {
		t.Fatalf("最近成功时间应只取模型成功：%v", panel.LastSummaryAt)
	}
	if panel.PendingPublish != 2 || panel.PendingPurge != 0 {
		t.Fatalf("投影任务计数不符：%+v", panel)
	}

	// 重建：即使没有新发言，下一轮补偿优先命中；不影响其他观众。
	if err := viewers.RequeueSummary(ctx, "6101"); err != nil {
		t.Fatal(err)
	}
	candidates, err := viewers.ActiveViewers(ctx, store.ViewerSummaryQuery{
		Since: base.Add(-24 * time.Hour), Now: wall.Add(time.Hour), MinHits: 2,
		MinNewMessages: 3, Limit: 10, BackfillLimit: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].UID != "6101" {
		t.Fatalf("重建未进入下一轮候选：%+v", candidates)
	}

	// 撤回：档案立即清空、记忆归档、登记 purge；另一个观众不受影响。
	if err := viewers.RevokeSummary(ctx, "6101"); err != nil {
		t.Fatal(err)
	}
	profile, _ := viewers.GetProfile(ctx, "6101")
	if profile.ProfileSummary != "" || !profile.SummaryUpdatedAt.IsZero() ||
		len(profile.SummarySourceIDs) != 0 || !profile.SummaryLastSuccessAt.IsZero() ||
		profile.SummaryFailures != 0 || !profile.SummaryNextRetryAt.IsZero() {
		t.Fatalf("撤回后侧写状态未清空：%+v", profile)
	}
	var archived bool
	if err := db.WithContext(ctx).Raw("SELECT archived FROM memory_records WHERE id = ?", "viewer-profile:6101").Scan(&archived).Error; err != nil {
		t.Fatal(err)
	}
	if !archived {
		t.Fatal("撤回后记忆行必须归档")
	}
	if action, _ := summaryJob(t, db, ctx, "6101"); action != store.ViewerSummaryActionPurge {
		t.Fatalf("撤回后应登记 purge：%s", action)
	}
	other, _ := viewers.GetProfile(ctx, "6102")
	if other.ProfileSummary == "" {
		t.Fatalf("撤回误伤其他观众：%+v", other)
	}
	panel, err = viewers.SummaryPanel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if panel.Summarized != 1 || panel.Sources != 2 || panel.PendingPublish != 1 || panel.PendingPurge != 1 {
		t.Fatalf("撤回后面板计数不符：%+v", panel)
	}
	// 重复撤回幂等；不存在的观众明确报 NotFound。
	if err := viewers.RevokeSummary(ctx, "6101"); err != nil {
		t.Fatalf("重复撤回应幂等：%v", err)
	}
	if err := viewers.RevokeSummary(ctx, "no-such"); !errors.Is(err, store.ErrViewerNotFound) {
		t.Fatalf("未知观众撤回应 NotFound：%v", err)
	}
	if err := viewers.RequeueSummary(ctx, "no-such"); !errors.Is(err, store.ErrViewerNotFound) {
		t.Fatalf("未知观众重建应 NotFound：%v", err)
	}
}

// TestViewerSummaryBackfillViewers 手动回填只选从未侧写、未在退避期的观众，
// 按 hits 倒序，并严格按请求人数截断（M03）。
func TestViewerSummaryBackfillViewers(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	viewers, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	seedSummaryViewer(t, events, "7001", base)
	seedViewerEvent(t, events, "s:7001-3", "7001", "侧写观众", "in", "第三句", "", base.Add(2*time.Minute))
	seedSummaryViewer(t, events, "7002", base)
	seedSummaryViewer(t, events, "7003", base)
	seedSummaryViewer(t, events, "7004", base)
	if _, err := viewers.RebuildProfiles(ctx, store.ViewerRebuildOptions{MinHits: 2, RecentTexts: 15, RecentReplies: 10}); err != nil {
		t.Fatal(err)
	}
	// 7004 已有侧写（更新路径候选），7003 补偿未到期。
	if err := db.WithContext(ctx).Exec("UPDATE viewer_profiles SET summary_updated_at=?, profile_summary='已有侧写', summary_attempted_at=? WHERE uid='7004'", base, base).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Exec("UPDATE viewer_profiles SET profile_summary_next_retry_at=? WHERE uid='7003'", base.Add(2*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}

	query := store.ViewerSummaryQuery{
		Since: base.Add(-24 * time.Hour), Now: base.Add(30 * time.Minute),
		MinHits: 2, MinNewMessages: 1, Limit: 10, BackfillLimit: 10,
	}
	got, err := viewers.BackfillViewers(ctx, query)
	if err != nil {
		t.Fatalf("回填候选查询失败：%v", err)
	}
	if len(got) != 2 || got[0].UID != "7001" || got[1].UID != "7002" {
		t.Fatalf("回填候选不符：%+v", got)
	}
	for _, profile := range got {
		if profile.ProfileSummary != "" {
			t.Fatalf("回填候选不应包含已有侧写：%+v", profile)
		}
	}

	query.Limit = 1
	got, err = viewers.BackfillViewers(ctx, query)
	if err != nil {
		t.Fatalf("限量回填失败：%v", err)
	}
	if len(got) != 1 || got[0].UID != "7001" {
		t.Fatalf("限量回填不符：%+v", got)
	}

	query.Limit = 0
	if _, err := viewers.BackfillViewers(ctx, query); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("limit=0 应拒绝：%v", err)
	}
}
