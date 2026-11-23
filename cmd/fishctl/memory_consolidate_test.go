package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/memory"
)

type fakeConsolidation struct {
	candidates []memory.ConsolidationCandidate
	applied    int
}

func (f *fakeConsolidation) Candidates(context.Context, int) ([]memory.ConsolidationCandidate, error) {
	return f.candidates, nil
}

func (f *fakeConsolidation) Apply(context.Context, []memory.ConsolidationAction) (int, error) {
	f.applied++
	return len(f.candidates), nil
}

// TestConsolidateDefaultsDryRun 默认只出计划，不写库。
func TestConsolidateDefaultsDryRun(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeConsolidation{candidates: []memory.ConsolidationCandidate{
		{ID: "low", Kind: "preference", Importance: 0.3, Confidence: 0.5, ValidFrom: now.Add(-60 * 24 * time.Hour)},
	}}
	var out bytes.Buffer
	if err := executeMemoryConsolidate(context.Background(), repo, false, &out); err != nil {
		t.Fatal(err)
	}
	if repo.applied != 0 {
		t.Fatal("dry-run 不应写库")
	}
	if !strings.Contains(out.String(), `"dry_run":true`) || !strings.Contains(out.String(), `"id":"low"`) {
		t.Fatalf("报告不完整：%s", out.String())
	}
}

// TestConsolidateApplyWritesOnlyConfirmedPlan --apply 才执行，并回显影响行数。
func TestConsolidateApplyWritesOnlyConfirmedPlan(t *testing.T) {
	now := time.Now().UTC()
	repo := &fakeConsolidation{candidates: []memory.ConsolidationCandidate{
		{ID: "low", Kind: "preference", Importance: 0.3, Confidence: 0.5, ValidFrom: now.Add(-60 * 24 * time.Hour)},
	}}
	var out bytes.Buffer
	if err := executeMemoryConsolidate(context.Background(), repo, true, &out); err != nil {
		t.Fatal(err)
	}
	if repo.applied != 1 {
		t.Fatal("--apply 未执行")
	}
	if !strings.Contains(out.String(), `"applied":1`) || !strings.Contains(out.String(), `"dry_run":false`) {
		t.Fatalf("执行结果不完整：%s", out.String())
	}
}
