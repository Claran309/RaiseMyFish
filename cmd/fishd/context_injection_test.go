package main

// 本文件测看板注入在统一快照里的读取、拼接与降级。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type fakeInjectionReader struct {
	rows    []store.WebInjection
	err     error
	gotNow  time.Time
	queries int
}

func (f *fakeInjectionReader) ActiveInjections(_ context.Context, now time.Time) ([]store.WebInjection, error) {
	f.queries++
	f.gotNow = now
	return f.rows, f.err
}

// TestBuildContextReadsInjection 注入经统一快照进入 ContextInput，多条按新到旧换行拼接。
func TestBuildContextReadsInjection(t *testing.T) {
	mem, _ := snapshotTestMemory()
	injections := &fakeInjectionReader{rows: []store.WebInjection{
		{ID: 2, Content: " 先说「喵」再回答。 "},
		{ID: 1, Content: "今天自称小鱼干。"},
	}}
	builder := newContextBuilder(snapshotTestPack(), mem, nil, nil, nil, nil, zap.NewNop())
	builder.injections = injections

	input := builder.BuildContext(context.Background(), snapshotTestEvent("在吗"), "", false)
	if injections.queries != 1 {
		t.Fatalf("每轮只应读取一次注入：%d", injections.queries)
	}
	if input.Injection != "先说「喵」再回答。\n今天自称小鱼干。" {
		t.Fatalf("注入拼接错误：%q", input.Injection)
	}
}

// TestInjectionTextSkipsAndCaps 空内容跳过，条数与总长都压在上限内；放不下的旧条目省略。
func TestInjectionTextSkipsAndCaps(t *testing.T) {
	rows := []store.WebInjection{{ID: 9, Content: "   "}}
	for i := 0; i < injectionMaxItems+2; i++ {
		rows = append(rows, store.WebInjection{ID: int64(i), Content: strings.Repeat("设", 10)})
	}
	injections := &fakeInjectionReader{rows: rows}
	builder := newContextBuilder(snapshotTestPack(), nil, nil, nil, nil, nil, zap.NewNop())
	builder.injections = injections
	text := builder.injectionText(context.Background())
	if got := len(strings.Split(text, "\n")); got != injectionMaxItems {
		t.Fatalf("注入条数应封顶 %d：%d", injectionMaxItems, got)
	}

	long := []store.WebInjection{
		{ID: 2, Content: strings.Repeat("旧", store.MaxInjectionRunes-1)},
		{ID: 1, Content: "新"},
	}
	builder.injections = &fakeInjectionReader{rows: long}
	text = builder.injectionText(context.Background())
	if text != long[0].Content {
		t.Fatalf("总长超预算时应省略放不下的旧条目：%q", text)
	}
}

// TestInjectionTextDegradesOnError 读库失败只降级为空注入并记日志，不阻塞回复。
func TestInjectionTextDegradesOnError(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	injections := &fakeInjectionReader{err: errors.New("db down")}
	builder := newContextBuilder(snapshotTestPack(), nil, nil, nil, nil, nil, zap.New(core))
	builder.injections = injections

	if text := builder.injectionText(context.Background()); text != "" {
		t.Fatalf("读取失败应返回空注入：%q", text)
	}
	if logs.Len() != 1 {
		t.Fatalf("读取失败应记一条 warn：%d", logs.Len())
	}
	entry := logs.All()[0]
	if entry.Message != "看板注入读取失败，本轮不注入" || entry.ContextMap()["action"] != "injection_context" {
		t.Fatalf("日志字段不符合预期：%+v", entry)
	}
}

// TestInjectionTextWithoutReader 未装配注入仓储时保持空注入。
func TestInjectionTextWithoutReader(t *testing.T) {
	builder := newContextBuilder(snapshotTestPack(), nil, nil, nil, nil, nil, zap.NewNop())
	if text := builder.injectionText(context.Background()); text != "" {
		t.Fatalf("未装配时不应注入：%q", text)
	}
}
