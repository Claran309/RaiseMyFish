package personaeval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type safeStyleMissGenerator struct{}

func (*safeStyleMissGenerator) Name() string { return "test:neutral" }
func (*safeStyleMissGenerator) Generate(context.Context, Input) (Generation, error) {
	return Generation{RawOutput: "内容已收到", DeliveredOutput: "内容已收到"}, nil
}

func TestV5RunSeparatesStyleAndPendingManualAcceptance(t *testing.T) {
	s := v5Scorer(t)
	f, err := Prepare(context.Background(), "../../personas/dafeiyu", "../../personas/dafeiyu/eval/cases.jsonl", s)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), f, s, &safeStyleMissGenerator{}, RunOptions{RunID: "layered-test", OutputDir: filepath.Join(t.TempDir(), "report"), SourceKind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if r.RawPassed != 35 || r.PassRateBasis != "automated_safety" || r.Reached90 || !r.ManualReviewRequired || r.ManualReviewStatus != "pending" {
		t.Fatalf("风格混入通过率或人工门禁缺失：%+v", r)
	}
	if len(r.RawStyleMetrics) == 0 || len(r.Results[0].RawStyleObservations) == 0 {
		t.Fatal("风格结果未记录")
	}
	for _, m := range r.RawStyleMetrics {
		if m.Key == "expect:情绪" && m.Applicable > 0 && m.Matched != 0 {
			t.Fatal("中性回复误计情绪")
		}
	}
}

func TestV5OfflineReportIncludesAllSafetyChecksAndStyleMetrics(t *testing.T) {
	s := v5Scorer(t)
	out := filepath.Join(t.TempDir(), "layers")
	r, err := RescoreExisting("../../plans/reports/persona-eval/20261031T143439Z-758949f2", out, s)
	if err != nil {
		t.Fatal(err)
	}
	if r.RawPassed != 35 || r.DeliveredPassed != 35 || r.ActualModelCalls != 0 || r.NewModelValidation {
		t.Fatalf("离线层计数错误：%+v", r)
	}
	if r.PassRateBasis != "automated_safety" || r.ManualReviewStatus != "pending" || !r.ManualReviewRequired || len(r.RawStyleMetrics) == 0 {
		t.Fatal("安全率口径或人工状态缺失")
	}
	activityMetric := false
	for _, m := range r.RawStyleMetrics {
		if m.Key == "forbid:活动断言" {
			activityMetric = true
			if m.Applicable != 35 || m.Matched != 1 {
				t.Fatalf("活动断言分母/命中错误：%+v", m)
			}
		}
	}
	if !activityMetric || r.RulesRevision != "activity-assertion-style-r1" {
		t.Fatal("缺少裁定版修订标记或活动观察汇总")
	}
	found := false
	for _, row := range r.Results {
		if row.ID == "p0h-dm-002" {
			found = true
			if !row.RawPass || len(row.RawStyleObservations) == 0 {
				t.Fatal("已发动态声明或风格未记录")
			}
		}
	}
	if !found {
		t.Fatal("缺少源用例")
	}
	md, err := os.ReadFile(filepath.Join(out, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "风格") || !strings.Contains(string(md), "人工") {
		t.Fatal("人类报告没有两层与人工提醒")
	}
}

func TestV5ConfiguredSensitiveValuesStillFailAndRedact(t *testing.T) {
	s := v5Scorer(t)
	f, err := Prepare(context.Background(), "../../personas/dafeiyu", "../../personas/dafeiyu/eval/cases.jsonl", s)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(context.Background(), f, s, &safeStyleMissGenerator{}, RunOptions{RunID: "layered-redaction-test", OutputDir: filepath.Join(t.TempDir(), "report"), SourceKind: "test", Secrets: []string{"内容已收到"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.RawPassed != 0 || !r.Results[0].Redacted || strings.Contains(r.Results[0].RawOutput, "内容已收到") {
		t.Fatal("v5漏掉未匹配词法模式的真实配置敏感值")
	}
	found := false
	for _, tag := range r.Results[0].RawFailures {
		if tag == "configured_credential_detected" {
			found = true
		}
	}
	if !found {
		t.Fatal("未保留配置敏感值安全失败标签")
	}
}

type brokenAssessmentGenerator struct {
	calls int
	empty bool
}

func (*brokenAssessmentGenerator) Name() string { return "test:broken" }
func (g *brokenAssessmentGenerator) Generate(context.Context, Input) (Generation, error) {
	g.calls++
	if g.calls == 1 {
		if g.empty {
			return Generation{}, nil
		}
		return Generation{}, errors.New("synthetic error")
	}
	return Generation{RawOutput: "内容已收到", DeliveredOutput: "内容已收到"}, nil
}

func TestV5IncompleteEvaluationCannotClaimSafetyThreshold(t *testing.T) {
	for _, empty := range []bool{true, false} {
		s := v5Scorer(t)
		f, err := Prepare(context.Background(), "../../personas/dafeiyu", "../../personas/dafeiyu/eval/cases.jsonl", s)
		if err != nil {
			t.Fatal(err)
		}
		r, err := Run(context.Background(), f, s, &brokenAssessmentGenerator{empty: empty}, RunOptions{RunID: "invalid-test", OutputDir: filepath.Join(t.TempDir(), "report"), SourceKind: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if r.Complete || r.AutomatedReached90 || r.RawSafetySummary == nil || r.RawSafetySummary.Assessed != 34 || r.RawSafetySummary.Unassessable != 1 || r.RawSafetySummary.Failed != 0 {
			t.Fatalf("错误被当作安全违规或安全达标：%+v", r)
		}
		if len(r.Results[0].RawFailures) > 0 || len(r.Results[0].RawEvaluationErrors) == 0 {
			t.Fatal("评估错误混在安全失败中")
		}
		text := markdown(r)
		if !strings.Contains(text, "无法评估") || !strings.Contains(text, "自动安全") {
			t.Fatal("未展示独立错误及自动安全口径")
		}
	}
}

func TestV5OfflineRejectsLegacyUnassessableOutput(t *testing.T) {
	legacy, err := NewScorerVersion("persona-lexical-v3")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Prepare(context.Background(), "../../personas/dafeiyu", "../../personas/dafeiyu/eval/cases.jsonl", legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, empty := range []bool{false, true} {
		source := filepath.Join(t.TempDir(), "legacy")
		if _, err := Run(context.Background(), f, legacy, &brokenAssessmentGenerator{empty: empty}, RunOptions{RunID: "legacy-errors", OutputDir: source, SourceKind: "test"}); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "safety-report")
		if _, err := RescoreExisting(source, target, v5Scorer(t)); err == nil {
			t.Fatal("把旧版完整运行中的无效输出计为安全率")
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("拒绝之前已发布安全报告")
		}
	}
}
