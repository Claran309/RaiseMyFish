package memory

import (
	"testing"
	"time"
)

func TestBuildConsolidationPlanArchiveRules(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	rows := []ConsolidationCandidate{
		{ID: "low", Kind: "preference", Importance: 0.3, Confidence: 0.5, ValidFrom: now.Add(-60 * 24 * time.Hour)},
		{ID: "high", Kind: "fact", Importance: 0.95, Confidence: 0.5, ValidFrom: now.Add(-60 * 24 * time.Hour)},
		{ID: "referenced", Kind: "fact", Importance: 0.3, Confidence: 0.5, ReferenceCount: 3, ValidFrom: now.Add(-60 * 24 * time.Hour)},
		{ID: "recent", Kind: "fact", Importance: 0.3, Confidence: 0.5, LastReferencedAt: now.Add(-time.Hour), ValidFrom: now.Add(-60 * 24 * time.Hour)},
		{ID: "confident", Kind: "fact", Importance: 0.3, Confidence: 0.9, ValidFrom: now.Add(-60 * 24 * time.Hour)},
	}
	plan := BuildConsolidationPlan(rows, ConsolidateOptions{Now: now})
	if !plan.DryRun || plan.Scanned != 5 {
		t.Fatalf("计划头错误：%+v", plan)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].ID != "low" || plan.Actions[0].Action != "archive" {
		t.Fatalf("归档建议不符：%+v", plan.Actions)
	}
}

func TestBuildConsolidationPlanDuplicateFactKey(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	rows := []ConsolidationCandidate{
		{ID: "new", FactKey: "k1", Importance: 0.8, ValidFrom: now.Add(-time.Hour)},
		{ID: "old", FactKey: "k1", Importance: 0.8, ValidFrom: now.Add(-48 * time.Hour)},
	}
	plan := BuildConsolidationPlan(rows, ConsolidateOptions{Now: now})
	found := false
	for _, action := range plan.Actions {
		if action.ID == "old" && action.Action == "supersede" {
			found = true
		}
		if action.ID == "new" && action.Action == "supersede" {
			t.Fatal("最新事实被建议替换")
		}
	}
	if !found {
		t.Fatalf("重复 fact_key 未提示：%+v", plan.Actions)
	}
}

func TestDecayScoreKeepsHighImportance(t *testing.T) {
	now := time.Now().UTC()
	if got := DecayScore(0.95, now.Add(-365*24*time.Hour), 0, now); got != 0.95 {
		t.Fatalf("高重要度不应衰减：%v", got)
	}
	if got := DecayScore(0.5, now.Add(-60*24*time.Hour), 0, now); got >= 0.5 {
		t.Fatalf("长期未引用应衰减：%v", got)
	}
	if got := DecayScore(0.5, now.Add(-60*24*time.Hour), 5, now); got <= DecayScore(0.5, now.Add(-60*24*time.Hour), 0, now) {
		t.Fatalf("引用次数应提升分数：%v", got)
	}
}
