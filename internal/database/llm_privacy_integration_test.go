//go:build integration

package database

// T03：未授权私密事件到达提炼 worker 后，必须在模型调用之前被拦截。
//
// 验收口径：真实 PG + mock LLM，长私信（>20 字且含 @）在 AllowPrivate=false 时
// LLM 调用 0 次、memory_records 写入 0 条；显式授权后才允许调用并落 private 记录。

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/memory/extractworker"
)

type t03LLM struct {
	calls int
	reply string
}

func (m *t03LLM) Complete(context.Context, string, string) (string, error) {
	m.calls++
	return m.reply, nil
}

func TestPrivateEventNeverReachesLLM(t *testing.T) {
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
	ev := doneEvent("dm:u42:t03", event.ChDM, "u42", true, at)
	ev.FromID = "2002"
	ev.FromName = "深夜观众"
	ev.Text = "@大肥鱼 我今天遇到好多事情，想慢慢跟你说一件很长的私事，千万别告诉别人"
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatalf("插入私密事件失败：%v", err)
	}

	llm := &t03LLM{reply: `{"archive":true,"records":[{"kind":"fact","title":"私事","summary":"观众深夜倾诉","importance":0.7}],"confidence":0.9}`}
	extractor, err := memory.NewLLMExtractor(llm, memory.LLMExtractOptions{MaxPerHour: 10})
	if err != nil {
		t.Fatalf("构造 LLM 提炼器失败：%v", err)
	}

	// AllowPrivate 零值 = false：默认拒绝。
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
	if stats.Scanned != 1 || stats.Skipped != 1 || stats.Extracted != 0 || stats.Inserted != 0 {
		t.Fatalf("统计 = %+v，期望 Scanned=1 Skipped=1 Extracted=0 Inserted=0", stats)
	}
	if llm.calls != 0 {
		t.Fatalf("未授权私密事件不应调用模型，实际 %d 次", llm.calls)
	}
	var count int64
	if err := db.WithContext(ctx).Table("memory_records").Count(&count).Error; err != nil {
		t.Fatalf("统计记忆记录失败：%v", err)
	}
	if count != 0 {
		t.Fatalf("未授权私密事件不应写入 memory_records，实际 %d 条", count)
	}

	// 水位已推进：同一 worker 再跑一轮不会重复扫描。
	again, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("第二轮 RunOnce 失败：%v", err)
	}
	if again.Scanned != 0 {
		t.Fatalf("第二轮不应重复扫描：%+v", again)
	}

	// 对照：显式授权后，同一事件才允许调用模型并落 private 记录。
	allowed, err := extractworker.New(src, memRepo, extractworker.NewMemoryWatermark(), extractworker.Options{
		Extractor:    extractor,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("构造授权 worker 失败：%v", err)
	}
	authorized, err := allowed.RunOnce(ctx)
	if err != nil {
		t.Fatalf("授权 RunOnce 失败：%v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("授权后应调用模型 1 次，实际 %d", llm.calls)
	}
	if authorized.Inserted != 1 {
		t.Fatalf("授权后应写入 1 条记录：%+v", authorized)
	}
	records, err := memRepo.ListRecords(ctx, 10)
	if err != nil {
		t.Fatalf("读取记忆记录失败：%v", err)
	}
	if len(records) != 1 {
		t.Fatalf("记录数 = %d，期望 1", len(records))
	}
	if !records[0].Private || records[0].Channel != event.ChDM || records[0].Session != "u42" {
		t.Fatalf("授权记录范围不符：%+v", records[0])
	}
}
