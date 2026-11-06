//go:build integration

package database

// M04/T05(T06)：LLM 提炼记录的主体与「明确否决」落库口径。
//
// 验收口径：真实 PG + mock LLM
//   - archive=true  → memory_records 带上事件的 from_id/from_name（模型不参与主体）；
//   - archive=false → 即使规则版能命中，也一条都不写。

import (
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/memory/extractworker"
)

// TestLLMRecordCarriesEventSubject 主体只来自事件，落库后能按 uid 查回。
func TestLLMRecordCarriesEventSubject(t *testing.T) {
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

	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	ev := doneEvent("live:room:m04-1", event.ChLive, "room", false, at)
	ev.FromID = "2002"
	ev.FromName = "熟客乙"
	ev.Text = "我今年大二，学计算机专业，平时喜欢写代码，周末喜欢打游戏和钓鱼，偶尔也看看直播放松一下"
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatalf("插入事件失败：%v", err)
	}

	llm := &t03LLM{reply: `{"archive":true,"records":[{"kind":"fact","title":"大二学生","summary":"我今年大二，学计算机","importance":0.7}],"confidence":0.9}`}
	extractor, err := memory.NewLLMExtractor(llm, memory.LLMExtractOptions{MaxPerHour: 10})
	if err != nil {
		t.Fatalf("构造 LLM 提炼器失败：%v", err)
	}
	worker, err := extractworker.New(src, memRepo, extractworker.NewMemoryWatermark(), extractworker.Options{
		Extractor: extractor,
	})
	if err != nil {
		t.Fatalf("构造 worker 失败：%v", err)
	}
	stats, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Inserted != 1 {
		t.Fatalf("应写入 1 条记录：%+v", stats)
	}

	rows, err := memRepo.ListRecords(ctx, 10)
	if err != nil {
		t.Fatalf("读取记录失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("记录数 = %d，期望 1", len(rows))
	}

	// 直接查库断言主体：ListRecords 的投影不含 from_id，
	// 用它判断主体会得到「假阴性」，这里必须看真列。
	var fromID, fromName string
	row := db.WithContext(ctx).Raw(
		"SELECT COALESCE(from_id,'') AS from_id, COALESCE(from_name,'') AS from_name FROM memory_records WHERE id = ?",
		rows[0].ID,
	).Row()
	if err := row.Scan(&fromID, &fromName); err != nil {
		t.Fatalf("读取主体列失败：%v", err)
	}
	if fromID != "2002" || fromName != "熟客乙" {
		t.Fatalf("LLM 记录主体应来自事件：from_id=%q from_name=%q", fromID, fromName)
	}

	// 主体可用于「这个人的事实」查询：按 uid 取回同一条。
	facts, err := memRepo.FactsByViewer(ctx, "2002", 10)
	if err != nil {
		t.Fatalf("按观众查事实失败：%v", err)
	}
	if len(facts) != 1 || facts[0].FromID != "2002" {
		t.Fatalf("按 uid 未查回：%+v", facts)
	}
}

// TestLLMArchiveFalseWritesNothing 模型明确否决时，规则版兜底也不许写。
func TestLLMArchiveFalseWritesNothing(t *testing.T) {
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

	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	ev := doneEvent("live:room:m04-2", event.ChLive, "room", false, at)
	ev.FromID = "2002"
	ev.FromName = "熟客乙"
	// 这句话命中规则版「自我陈述」（含「我是」），模型否决后不该落库。
	ev.Text = "我是学生，今天也在上课，早上八点就有课，一直上到中午才下课，累死了"
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatalf("插入事件失败：%v", err)
	}

	llm := &t03LLM{reply: `{"archive":false,"records":[],"confidence":0}`}
	extractor, err := memory.NewLLMExtractor(llm, memory.LLMExtractOptions{MaxPerHour: 10})
	if err != nil {
		t.Fatalf("构造 LLM 提炼器失败：%v", err)
	}
	worker, err := extractworker.New(src, memRepo, extractworker.NewMemoryWatermark(), extractworker.Options{
		Extractor: extractor,
	})
	if err != nil {
		t.Fatalf("构造 worker 失败：%v", err)
	}
	stats, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("应调用模型 1 次，实际 %d", llm.calls)
	}
	if stats.Inserted != 0 {
		t.Fatalf("明确否决不应写入：%+v", stats)
	}
	var count int64
	if err := db.WithContext(ctx).Table("memory_records").Count(&count).Error; err != nil {
		t.Fatalf("统计记录失败：%v", err)
	}
	if count != 0 {
		t.Fatalf("archive=false 仍写入了 %d 条记录", count)
	}
}
