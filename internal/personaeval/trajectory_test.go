package personaeval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckTrajectoryMetrics(t *testing.T) {
	c := TrajectoryCase{
		ID: "t1", Input: "记得我吗", ShouldUseTools: true,
		ExpectedTools: []string{"memory_search"}, ForbiddenTools: []string{"web_search"},
	}
	trajectory := &Trajectory{
		Steps: 2, EvidenceCount: 1,
		Tools: []ToolCallRecord{{Name: "memory_search", OK: true}},
	}
	report, skip := CheckTrajectory(c, trajectory)
	if skip != "" || report.ToolSelectionAccuracy.Value != 1 || report.UnnecessaryToolRate.Value != 0 ||
		report.AvgSteps.Value != 2 || report.Groundedness.Value != 1 || report.HallucinationRate.Value != 0 {
		t.Fatalf("轨迹指标错误：%+v skip=%s", report, skip)
	}

	// 调了禁用工具 → 幻觉率 1；多调无关工具 → 不必要工具率上升。
	trajectory.Tools = append(trajectory.Tools, ToolCallRecord{Name: "web_search"})
	report, _ = CheckTrajectory(c, trajectory)
	if report.HallucinationRate.Value != 1 || report.UnnecessaryToolRate.Value <= 0 {
		t.Fatalf("禁用/多余工具未计分：%+v", report)
	}

	// 无轨迹必须标 skip，不能按 0 分混进分母。
	if _, skip := CheckTrajectory(c, nil); skip == "" {
		t.Fatal("缺轨迹未标 skip")
	}
}

func TestMergeTrajectoryKeepsDenominators(t *testing.T) {
	first, _ := CheckTrajectory(
		TrajectoryCase{ID: "a", Input: "x", ShouldUseTools: true, ExpectedTools: []string{"memory_search"}},
		&Trajectory{Steps: 2, EvidenceCount: 1, Tools: []ToolCallRecord{{Name: "memory_search"}}},
	)
	second, _ := CheckTrajectory(
		TrajectoryCase{ID: "b", Input: "y", ShouldUseTools: true, ExpectedTools: []string{"history"}},
		&Trajectory{Steps: 4, EvidenceCount: 1, Tools: []ToolCallRecord{{Name: "memory_search"}}},
	)
	merged := MergeTrajectory([]TrajectoryReport{first, second}, []string{"c"})
	// Cases 是总用例数（已评估 + skip），Evaluated 只数有轨迹的。
	if merged.Cases != 3 || merged.Evaluated != 2 || len(merged.Skipped) != 1 {
		t.Fatalf("合并分母错误：%+v", merged)
	}
	if merged.ToolSelectionAccuracy.Value != 0.5 || merged.AvgSteps.Value != 3 {
		t.Fatalf("合并指标错误：%+v", merged)
	}
}

func TestValidateTrajectoryCasesRejectsUnknownTool(t *testing.T) {
	err := ValidateTrajectoryCases([]TrajectoryCase{
		{ID: "x", Input: "问", ShouldUseTools: true, ExpectedTools: []string{"not_a_tool"}},
	}, TrajectoryToolNames)
	if err == nil {
		t.Fatal("未注册工具未被拒绝")
	}
}

func TestLoadTrajectoryCasesFromRepo(t *testing.T) {
	cases, err := LoadTrajectoryCases("../../personas/dafeiyu/eval/trajectory-cases.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 50 {
		t.Fatalf("轨迹用例数=%d，期望50", len(cases))
	}
	if err := ValidateTrajectoryCases(cases, TrajectoryToolNames); err != nil {
		t.Fatal(err)
	}
}

func TestScoreTrajectoryWithRecordedRuns(t *testing.T) {
	cases := []TrajectoryCase{
		{ID: "a", Input: "记得我吗", ShouldUseTools: true, ExpectedTools: []string{"memory_search"}},
		{ID: "b", Input: "早", ShouldUseTools: false, ForbiddenTools: []string{"web_search"}},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	body := `{"case_id":"a","trajectory":{"steps":2,"evidence_count":1,"tools":[{"name":"memory_search","ok":true}]}}
{"case_id":"b","trajectory":{"steps":1,"tools":[]}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, err := LoadTrajectoryRuns(path)
	if err != nil {
		t.Fatal(err)
	}
	report := ScoreTrajectory(cases, runs)
	if report.Evaluated != 2 || report.ToolSelectionAccuracy.Value != 1 || report.UnnecessaryToolRate.Value != 0 || report.HallucinationRate.Value != 0 {
		t.Fatalf("录制轨迹评分错误：%+v", report)
	}
}

func TestScoreTrajectoryMarksMissingRuns(t *testing.T) {
	cases := []TrajectoryCase{{ID: "a", Input: "记得我吗", ShouldUseTools: true, ExpectedTools: []string{"memory_search"}}}
	report := ScoreTrajectory(cases, map[string]*Trajectory{})
	if report.Evaluated != 0 || len(report.Skipped) != 1 {
		t.Fatalf("缺轨迹未 skip：%+v", report)
	}
}
