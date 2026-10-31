package personaeval

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRescoreOldRunDoesNotCallModelOrAlterOriginal(t *testing.T) {
	s, err := NewScorerVersion("persona-lexical-v2")
	if err != nil {
		t.Fatal(err)
	}
	source := "../../plans/reports/persona-eval/20261031T105248Z-501d0282"
	r, err := RescoreExisting(source, filepath.Join(t.TempDir(), "v2"), s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 35 || r.RawPassed != 17 || r.DeliveredPassed != 17 || r.ActualModelCalls != 0 || r.NewModelValidation || r.SourceKind != "rescore_existing_model_outputs" {
		t.Fatalf("离线重评被当作真实新模型评估：%+v", r)
	}
	if _, err := RescoreExisting(source, filepath.Join(source, "must-not-write"), s); err == nil {
		t.Fatal("重评分不应写入旧run目录")
	}
}

func TestTestSourceCannotBecomeRealRescore(t *testing.T) {
	s, err := NewScorer()
	if err != nil {
		t.Fatal(err)
	}
	f, err := Prepare(context.Background(), "../../personas/dafeiyu", "../../personas/dafeiyu/eval/cases.jsonl", s)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "test-run")
	if _, err := Run(context.Background(), f, s, &fakeGenerator{}, RunOptions{RunID: "test-source", OutputDir: source, SourceKind: "test"}); err != nil {
		t.Fatal(err)
	}
	r, err := RescoreExisting(source, source+"-rescore", s)
	if err != nil {
		t.Fatal(err)
	}
	if r.SourceKind != "rescore_test_outputs" {
		t.Fatal("测试来源被包装为真实旧模型输出")
	}
}
