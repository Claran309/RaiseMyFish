package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

type fakeMemoryUsageRepo struct {
	rows [][]store.MemoryUsage
	err  error
}

func (f *fakeMemoryUsageRepo) RecordMemoryUsage(_ context.Context, rows []store.MemoryUsage) error {
	f.rows = append(f.rows, rows)
	return f.err
}

func (f *fakeMemoryUsageRepo) MarkMemoryUsed(context.Context, string, []string, bool) error {
	return nil
}

// TestWithMemoryMetaOnlyIDs 回复 meta 只带记忆 ID，不带正文。
func TestWithMemoryMetaOnlyIDs(t *testing.T) {
	reply := withMemoryMeta(brain.Reply{Text: "在的", Meta: json.RawMessage(`{"k":"v"}`)}, []string{"fact:1", "fact:2"})
	var fields map[string]any
	if err := json.Unmarshal(reply.Meta, &fields); err != nil {
		t.Fatal(err)
	}
	ids, ok := fields["memory_ids"].([]any)
	if !ok || len(ids) != 2 || ids[0] != "fact:1" || fields["k"] != "v" {
		t.Fatalf("meta 未合并 memory_ids：%+v", fields)
	}
}

// TestRecordMemoryUsageRanksAndToleratesFailure 记录失败只降级，不影响发送。
func TestRecordMemoryUsageRanksAndToleratesFailure(t *testing.T) {
	repo := &fakeMemoryUsageRepo{}
	recordMemoryUsage(context.Background(), repo, zap.NewNop(), "live:1:1", []string{"a", "b"})
	if len(repo.rows) != 1 || len(repo.rows[0]) != 2 || repo.rows[0][1].Rank != 1 {
		t.Fatalf("归因行不符：%+v", repo.rows)
	}
	repo = &fakeMemoryUsageRepo{err: errors.New("db down")}
	recordMemoryUsage(context.Background(), repo, zap.NewNop(), "live:1:1", []string{"a"})
}

func TestMemoryIDsDedup(t *testing.T) {
	ids := memoryIDs([]memory.Record{{ID: "a"}, {ID: "a"}, {ID: ""}, {ID: "b"}})
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ID 去重失败：%v", ids)
	}
}
