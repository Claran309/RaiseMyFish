//go:build integration

package database

// 本文件测「观众相关记忆」查询（当前发言者主动注入用）：
// 关系口径（from_id=观众 / subject=viewer 且 asserted_by=观众）、
// 范围与隐私过滤、归档/失效/未来记录的排除、排序与 limit。

import (
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

func newViewerRecord(id, summary string, importance float64, from time.Time) store.MemoryRecord {
	return store.MemoryRecord{
		Record: memory.Record{
			ID: id, Channel: event.ChLive, Session: "30308220", Summary: summary,
			Importance: importance, Confidence: 0.9, ValidFrom: from,
		},
		Source: "persona", Kind: "talk", Title: "观众相关记忆",
	}
}

// TestViewerRecordsRelationAndVisibility 验证关系口径与隐私/时效过滤。
func TestViewerRecordsRelationAndVisibility(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	const uid = "9001"
	now := time.Now().UTC().Truncate(time.Microsecond)
	rows := []store.MemoryRecord{
		// 1. 观众自述（from_id=观众）
		func() store.MemoryRecord {
			r := newViewerRecord("vm-own", "我昨晚顶号了", 0.5, now.Add(-2*time.Hour))
			r.FromID, r.FromName = uid, "熟客甲"
			r.Subject, r.AssertedByID, r.AssertedByName = memory.SoulSubjectViewer, uid, "熟客甲"
			return r
		}(),
		// 2. 主体是观众、断言人也是观众（from_id 缺失的旧写法）
		func() store.MemoryRecord {
			r := newViewerRecord("vm-asserted", "自己说爱吃鱼", 0.7, now.Add(-4*time.Hour))
			r.Subject, r.AssertedByID, r.AssertedByName = memory.SoulSubjectViewer, uid, "熟客甲"
			return r
		}(),
		// 3. 别的观众 → 不返回
		func() store.MemoryRecord {
			r := newViewerRecord("vm-other", "别人的事", 0.9, now.Add(-time.Hour))
			r.FromID, r.Subject, r.AssertedByID = "9002", memory.SoulSubjectViewer, "9002"
			return r
		}(),
		// 4. 侧写记录由档案段注入，本查询排除避免重复
		func() store.MemoryRecord {
			r := newViewerRecord("vm-profile", "侧写：爱吃白饭", 0.8, now.Add(-30*time.Minute))
			r.Kind = "viewer_profile"
			r.FromID, r.Subject, r.AssertedByID = uid, memory.SoulSubjectViewer, uid
			return r
		}(),
		// 5. 私密 DM：公开视角不可见，同会话私密视角可见
		func() store.MemoryRecord {
			r := newViewerRecord("vm-dm", "私信里说了心事", 0.6, now.Add(-3*time.Hour))
			r.Channel, r.Session, r.Private = event.ChDM, "dm:9001", true
			r.FromID, r.Subject, r.AssertedByID = uid, memory.SoulSubjectViewer, uid
			return r
		}(),
		// 6. 别的 DM 会话的私密记录 → 本会话不可见
		func() store.MemoryRecord {
			r := newViewerRecord("vm-dm-other", "另一个私信会话", 0.6, now.Add(-3*time.Hour))
			r.Channel, r.Session, r.Private = event.ChDM, "dm:9002", true
			r.FromID, r.Subject, r.AssertedByID = uid, memory.SoulSubjectViewer, uid
			return r
		}(),
	}
	for _, row := range rows {
		if ok, err := repo.InsertRecord(ctx, row); err != nil || !ok {
			t.Fatalf("写入 %s 失败：%v / %v", row.ID, ok, err)
		}
	}

	// 归档、失效、未来生效：都不应返回。
	for _, row := range []store.MemoryRecord{
		func() store.MemoryRecord {
			r := newViewerRecord("vm-archived", "已归档的旧事", 0.9, now.Add(-5*time.Hour))
			r.FromID = uid
			return r
		}(),
		func() store.MemoryRecord {
			r := newViewerRecord("vm-expired", "已失效的旧事", 0.9, now.Add(-6*time.Hour))
			r.FromID = uid
			return r
		}(),
		func() store.MemoryRecord {
			r := newViewerRecord("vm-future", "还没发生的事", 0.9, now.Add(time.Hour))
			r.FromID = uid
			return r
		}(),
	} {
		if ok, err := repo.InsertRecord(ctx, row); err != nil || !ok {
			t.Fatalf("写入 %s 失败：%v / %v", row.ID, ok, err)
		}
	}
	if err := db.WithContext(ctx).Exec("UPDATE memory_records SET archived = true WHERE id = ?", "vm-archived").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Exec("UPDATE memory_records SET valid_to = ? WHERE id = ?", now.Add(-30*time.Minute), "vm-expired").Error; err != nil {
		t.Fatal(err)
	}

	scope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	got, err := repo.ViewerRecords(ctx, memory.ViewerRecordQuery{Scope: scope, UID: uid, Limit: 10})
	if err != nil {
		t.Fatalf("查询观众相关记忆失败：%v", err)
	}
	want := []string{"vm-own", "vm-asserted"}
	if len(got) != len(want) {
		t.Fatalf("返回 %d 条，期望 %d：%+v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("第 %d 条 = %q，期望 %q（应按 valid_from 新的在前）", i, got[i].ID, id)
		}
	}
	if len(got[0].EventIDs) != 0 || got[0].Importance != 0.5 {
		t.Errorf("字段映射不符：%+v", got[0])
	}

	// limit 生效
	limited, err := repo.ViewerRecords(ctx, memory.ViewerRecordQuery{Scope: scope, UID: uid, Limit: 1})
	if err != nil || len(limited) != 1 || limited[0].ID != "vm-own" {
		t.Fatalf("limit 未生效：%+v / %v", limited, err)
	}

	// 私密 DM：公开视角看不到；同一 DM 会话的私密视角除公开记录外还能看到本会话私密。
	privateScope := memory.Scope{Channel: event.ChDM, Session: "dm:9001", Private: true}
	dm, err := repo.ViewerRecords(ctx, memory.ViewerRecordQuery{Scope: privateScope, UID: uid, Limit: 10})
	if err != nil {
		t.Fatalf("私密视角查询失败：%v", err)
	}
	ids := map[string]bool{}
	for _, record := range dm {
		ids[record.ID] = true
	}
	if !ids["vm-dm"] || !ids["vm-own"] {
		t.Fatalf("私密同会话应能看到本会话私密与公开记录：%+v", dm)
	}
	if ids["vm-dm-other"] {
		t.Fatalf("别的 DM 会话的私密记录泄漏：%+v", dm)
	}
	otherDM, err := repo.ViewerRecords(ctx, memory.ViewerRecordQuery{
		Scope: memory.Scope{Channel: event.ChDM, Session: "dm:9002", Private: true}, UID: uid, Limit: 10,
	})
	if err != nil || len(otherDM) == 0 {
		t.Fatalf("另一个 DM 会话应返回自己的记录：%+v / %v", otherDM, err)
	}
	for _, record := range otherDM {
		if record.ID == "vm-dm" {
			t.Fatalf("跨会话私密记录泄漏：%+v", otherDM)
		}
	}
}

// TestViewerRecordsRejectsBadInput 空 UID / 超长 UID / 非法范围在查询前拒绝。
func TestViewerRecordsRejectsBadInput(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	scope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	long := make([]rune, focusSpeakerMaxRunes+1)
	for i := range long {
		long[i] = 'x'
	}
	cases := []memory.ViewerRecordQuery{
		{Scope: scope, UID: "", Limit: 10},
		{Scope: scope, UID: string(long), Limit: 10},
		{Scope: memory.Scope{Channel: event.ChLive, Session: "", Private: false}, UID: "9001", Limit: 10},
		{Scope: scope, UID: "9001", Limit: 0},
	}
	for i, tc := range cases {
		if _, err := repo.ViewerRecords(ctx, tc); err == nil {
			t.Errorf("非法参数 %d 未拒绝：%+v", i, tc)
		}
	}
}
