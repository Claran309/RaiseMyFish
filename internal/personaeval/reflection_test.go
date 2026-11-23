package personaeval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReflectionCasesFromRepo(t *testing.T) {
	cases, err := LoadReflectionCases("../../personas/dafeiyu/eval/reflection-cases.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 50 {
		t.Fatalf("反思用例数=%d，期望50", len(cases))
	}
	classes := map[string]int{}
	for _, c := range cases {
		classes[c.ExpectReflect]++
	}
	for _, want := range []string{"该改", "不该改", "预算边界", "安全边界"} {
		if classes[want] == 0 {
			t.Fatalf("缺少类别 %s：%+v", want, classes)
		}
	}
}

func TestScoreReflectionMetrics(t *testing.T) {
	cases := []ReflectionCase{
		{ID: "a", Input: "问", ExpectReflect: "该改"},
		{ID: "b", Input: "早", ExpectReflect: "不该改"},
		{ID: "c", Input: "缺", ExpectReflect: "该改"},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	body := `{"case_id":"a","reflect":true,"changed":true,"verdict":"improved"}
{"case_id":"b","reflect":true,"changed":true,"verdict":"harmed"}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	runs := map[string]ReflectionRun{}
	for _, line := range []string{`{"case_id":"a","reflect":true,"changed":true,"verdict":"improved"}`, `{"case_id":"b","reflect":true,"changed":true,"verdict":"harmed"}`} {
		var run ReflectionRun
		if err := json.Unmarshal([]byte(line), &run); err != nil {
			t.Fatal(err)
		}
		runs[run.CaseID] = run
	}
	report := ScoreReflection(cases, runs)
	if report.Evaluated != 2 || len(report.Skipped) != 1 {
		t.Fatalf("分母错误：%+v", report)
	}
	if report.ChangedRate != 1 || report.ImproveRate != 0.5 || report.HarmRate != 0.5 || !report.HarmAlarm || report.JudgePending {
		t.Fatalf("指标错误：%+v", report)
	}
}
