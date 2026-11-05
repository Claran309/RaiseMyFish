//go:build integration

package database

// T04：事实去重与冲突更新不跨范围。
//
// 三组场景：
//  1. 提炼链路：同一 UID 同一句事实分别出现在公开弹幕、DM-A、DM-B，
//     必须落三条互不覆盖的记录，公开投影/事实查询/灵魂候选只见公开那条；
//  2. 冲突闸门：同主键但 private/channel/session 不同的写入必须拒绝，
//     原行保持不动；逆序写入（旧事件晚到）同样不得覆盖新事实；
//  3. 补提炼回放：新事实先落库，旧事件晚到重扫后摘要仍是新的。

import (
	"context"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/memory/extractworker"
	"RaiseMyFish/internal/store"
)

// assertStoredFact 按主键读回记录，逐项核对范围与摘要。
func assertStoredFact(t *testing.T, ctx context.Context, repo *MemoryRepo, id, summary string, private bool, ch event.Channel, session string, at time.Time) {
	t.Helper()
	rows, err := repo.ListRecords(ctx, memoryRecordLimitMax)
	if err != nil {
		t.Fatalf("读取记忆记录失败：%v", err)
	}
	for _, row := range rows {
		if row.ID != id {
			continue
		}
		if row.Summary != summary || row.Private != private || row.Channel != ch || row.Session != session {
			t.Fatalf("记录被跨范围改写：%+v", row)
		}
		if !row.ValidFrom.Equal(at) {
			t.Fatalf("valid_from = %v，期望 %v", row.ValidFrom, at)
		}
		return
	}
	t.Fatalf("记录 %s 不存在", id)
}

// TestWorkerFactScopesDoNotOverwrite 同一 UID 的同句事实在公开/DM-A/DM-B 各留一条。
func TestWorkerFactScopesDoNotOverwrite(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	memRepo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	src, err := NewDoneSourceRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	fixtures := []event.Event{
		doneEvent("live:room:fact1", event.ChLive, "room", false, base),
		doneEvent("dm:dm-a:fact1", event.ChDM, "dm-a", true, base.Add(time.Second)),
		doneEvent("dm:dm-b:fact1", event.ChDM, "dm-b", true, base.Add(2*time.Second)),
	}
	for i := range fixtures {
		fixtures[i].FromID = "2002"
		fixtures[i].FromName = "熟客乙"
		fixtures[i].Text = "我是学生"
		if _, err := events.Insert(ctx, fixtures[i]); err != nil {
			t.Fatalf("插入事件 %s 失败：%v", fixtures[i].ID, err)
		}
	}

	worker, err := extractworker.New(src, memRepo, extractworker.NewMemoryWatermark(), extractworker.Options{AllowPrivate: true})
	if err != nil {
		t.Fatalf("构造 worker 失败：%v", err)
	}
	stats, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Inserted != 3 || stats.Duplicated != 0 {
		t.Fatalf("统计 = %+v，期望 Inserted=3 Duplicated=0", stats)
	}

	rows, err := memRepo.ListRecords(ctx, 10)
	if err != nil {
		t.Fatalf("读取记忆记录失败：%v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("记录数 = %d，期望 3（公共/私密A/私密B 互不覆盖）", len(rows))
	}
	want := []struct {
		prefix  string
		private bool
		channel event.Channel
		session string
	}{
		{"fact:public:2002:", false, event.ChLive, "room"},
		{"fact:private:dm:dm-a:2002:", true, event.ChDM, "dm-a"},
		{"fact:private:dm:dm-b:2002:", true, event.ChDM, "dm-b"},
	}
	publicID := ""
	for _, w := range want {
		found := false
		for _, row := range rows {
			if !strings.HasPrefix(row.ID, w.prefix) {
				continue
			}
			found = true
			if row.Private != w.private || row.Channel != w.channel || row.Session != w.session {
				t.Errorf("%s 范围错误：%+v", w.prefix, row)
			}
			if row.Summary != "我是学生" {
				t.Errorf("%s 摘要不符：%q", w.prefix, row.Summary)
			}
			if !w.private {
				publicID = row.ID
			}
		}
		if !found {
			t.Errorf("缺少 %s 的记录：%+v", w.prefix, rows)
		}
	}

	// 公开投影只看得到公开那条：认人、灵魂候选都不能带出 DM 摘要。
	facts, err := memRepo.FactsByViewer(ctx, "2002", 10)
	if err != nil {
		t.Fatalf("按观众查事实失败：%v", err)
	}
	if len(facts) != 1 || facts[0].ID != publicID || facts[0].Private {
		t.Fatalf("公开事实查询串入私密记录：%+v", facts)
	}
	publicRecords, err := memRepo.Records(ctx, memory.Scope{Channel: event.ChLive, Session: "room"}, 10)
	if err != nil || len(publicRecords) != 1 || publicRecords[0].ID != publicID {
		t.Fatalf("公开召回 = %+v / %v，期望只有 %s", publicRecords, err, publicID)
	}
	soul, err := memRepo.SoulCandidates(ctx, 0.5, 10)
	if err != nil || len(soul) != 1 || soul[0].ID != publicID {
		t.Fatalf("灵魂候选串入私密记录：%+v / %v", soul, err)
	}

	// DM-A 范围包含公开行 + 自己的私密行，但绝不能出现 DM-B。
	scopeA := memory.Scope{Channel: event.ChDM, Session: "dm-a", Private: true}
	rowsA, err := memRepo.Records(ctx, scopeA, 10)
	if err != nil {
		t.Fatalf("DM-A 召回失败：%v", err)
	}
	if len(rowsA) != 2 {
		t.Fatalf("DM-A 召回 %d 条，期望 2（公开 + 本会话）", len(rowsA))
	}
	for _, row := range rowsA {
		if row.Session == "dm-b" {
			t.Fatalf("DM-A 召回到 DM-B 记录：%+v", row)
		}
	}
}

// TestInsertRecordRejectsCrossScopeAndStale 直接压 InsertRecord 的冲突闸门。
func TestInsertRecordRejectsCrossScopeAndStale(t *testing.T) {
	db, ctx := repositoryDB(t)
	memRepo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)

	fact := func(id string, ch event.Channel, session string, private bool, summary string, at time.Time) store.MemoryRecord {
		return store.MemoryRecord{
			Record: memory.Record{ID: id, Channel: ch, Session: session, Private: private,
				Summary: summary, Importance: 0.6, Confidence: 0.85,
				EventIDs: []string{"evt:" + summary}, ValidFrom: at},
			Source: "persona", Kind: "fact", Title: "事实：我是学生", FromID: "2002", FromName: "熟客乙",
		}
	}
	insert := func(m store.MemoryRecord, want bool) {
		t.Helper()
		got, err := memRepo.InsertRecord(ctx, m)
		if err != nil {
			t.Fatalf("写入 %s 报错：%v", m.ID, err)
		}
		if got != want {
			t.Fatalf("写入 %s 返回 %v，期望 %v", m.ID, got, want)
		}
	}

	// 公共行已存在：同主键的私密写入必须拒绝，公开摘要不能被私密摘要覆盖。
	publicID := "fact:guard:public-private"
	insert(fact(publicID, event.ChLive, "room", false, "公开版", base), true)
	insert(fact(publicID, event.ChDM, "dm-a", true, "私密A版", base.Add(time.Hour)), false)
	assertStoredFact(t, ctx, memRepo, publicID, "公开版", false, event.ChLive, "room", base)

	// 私密A已存在：私密B、公开写法都不能覆盖。
	dmID := "fact:guard:dm-dm"
	insert(fact(dmID, event.ChDM, "dm-a", true, "A版", base), true)
	insert(fact(dmID, event.ChDM, "dm-b", true, "B版", base.Add(time.Hour)), false)
	insert(fact(dmID, event.ChLive, "room", false, "公开版", base.Add(2*time.Hour)), false)
	assertStoredFact(t, ctx, memRepo, dmID, "A版", true, event.ChDM, "dm-a", base)

	// 逆序补提炼：旧事件晚到不得覆盖新事实；更晚的事件才允许更新。
	orderID := "fact:guard:order"
	insert(fact(orderID, event.ChLive, "room", false, "新说法", base.Add(time.Hour)), true)
	insert(fact(orderID, event.ChLive, "room", false, "旧说法", base), false)
	assertStoredFact(t, ctx, memRepo, orderID, "新说法", false, event.ChLive, "room", base.Add(time.Hour))
	insert(fact(orderID, event.ChLive, "room", false, "更新说法", base.Add(2*time.Hour)), true)
	assertStoredFact(t, ctx, memRepo, orderID, "更新说法", false, event.ChLive, "room", base.Add(2*time.Hour))

	// 非事实类即使撞上同主键也不能改写事实行。
	promise := fact(orderID, event.ChLive, "room", false, "承诺版", base.Add(3*time.Hour))
	promise.Kind = "promise"
	insert(promise, false)
	assertStoredFact(t, ctx, memRepo, orderID, "更新说法", false, event.ChLive, "room", base.Add(2*time.Hour))
}

// TestWorkerBackfillDoesNotOverwriteNewerFact 新事实先落库，旧事件晚到重扫不得改写。
func TestWorkerBackfillDoesNotOverwriteNewerFact(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	memRepo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	src, err := NewDoneSourceRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	// 两条文本共享前 60+ 个字符：规则版标题被截断后相同 → 事实主键相同，
	// 但摘要保留到 200 字，能区分新旧两句话。
	common := "我是学生" + strings.Repeat("甲", 60)
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	newer := doneEvent("live:room:newer", event.ChLive, "room", false, base.Add(time.Hour))
	newer.FromID, newer.FromName = "2002", "熟客乙"
	newer.Text = common + "新"
	if _, err := events.Insert(ctx, newer); err != nil {
		t.Fatalf("插入新事件失败：%v", err)
	}

	first, err := extractworker.New(src, memRepo, extractworker.NewMemoryWatermark(), extractworker.Options{})
	if err != nil {
		t.Fatalf("构造 worker 失败：%v", err)
	}
	stats, err := first.RunOnce(ctx)
	if err != nil || stats.Inserted != 1 {
		t.Fatalf("首轮提炼失败：%+v / %v", stats, err)
	}

	// 旧事件晚到：用零水位 worker 重扫（模拟回放/补提炼），顺序是先旧后新。
	older := doneEvent("live:room:older", event.ChLive, "room", false, base)
	older.FromID, older.FromName = "2002", "熟客乙"
	older.Text = common + "旧"
	if _, err := events.Insert(ctx, older); err != nil {
		t.Fatalf("插入旧事件失败：%v", err)
	}

	backfill, err := extractworker.New(src, memRepo, extractworker.NewMemoryWatermark(), extractworker.Options{})
	if err != nil {
		t.Fatalf("构造补提炼 worker 失败：%v", err)
	}
	stats, err = backfill.RunOnce(ctx)
	if err != nil {
		t.Fatalf("补提炼 RunOnce 失败：%v", err)
	}
	if stats.Inserted != 0 || stats.Duplicated != 2 {
		t.Fatalf("补提炼统计 = %+v，期望 Inserted=0 Duplicated=2（旧被拒、新幂等）", stats)
	}

	rows, err := memRepo.ListRecords(ctx, 10)
	if err != nil {
		t.Fatalf("读取记忆记录失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("记录数 = %d，期望 1", len(rows))
	}
	if rows[0].Summary != newer.Text {
		t.Fatalf("旧事实覆盖了新事实：%q", rows[0].Summary)
	}
	if !rows[0].ValidFrom.Equal(newer.At) {
		t.Fatalf("valid_from = %v，期望 %v", rows[0].ValidFrom, newer.At)
	}
}
