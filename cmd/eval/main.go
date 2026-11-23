// eval 是统一离线评测入口：人格 fixture 回放 + 坏回复检测 + 知识检索离线评测。
//
// 只跑本地文件与确定性评分，不读 .env、不连数据库、不调用真实模型；
// 因此可以直接进 push CI。真实模型评测仍走 fishctl persona eval。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"RaiseMyFish/internal/knowledgeeval"
	"RaiseMyFish/internal/personaeval"
)

// Summary 是一次评测的总览；字段全部可由本文件重新计算。
type Summary struct {
	RunID          string                      `json:"run_id"`
	Mode           string                      `json:"mode"`
	StartedAt      time.Time                   `json:"started_at"`
	FinishedAt     time.Time                   `json:"finished_at"`
	Persona        *PersonaSummary             `json:"persona,omitempty"`
	BadReplies     *personaeval.BadReplyReport `json:"bad_replies,omitempty"`
	Knowledge      *KnowledgeSummary           `json:"knowledge,omitempty"`
	Passed         bool                        `json:"passed"`
	FailureReasons []string                    `json:"failure_reasons,omitempty"`
	Notes          []string                    `json:"notes,omitempty"`
}

// PersonaSummary 只保留可对比指标，详细结果在子目录 report.json。
type PersonaSummary struct {
	SourceKind         string  `json:"source_kind"`
	RulesVersion       string  `json:"rules_version"`
	Total              int     `json:"total"`
	Completed          int     `json:"completed"`
	RawPassed          int     `json:"raw_passed"`
	DeliveredPassed    int     `json:"delivered_passed"`
	RawPassRate        float64 `json:"raw_pass_rate"`
	DeliveredPassRate  float64 `json:"delivered_pass_rate"`
	Complete           bool    `json:"complete"`
	PassRateBasis      string  `json:"pass_rate_basis,omitempty"`
	ManualReviewStatus string  `json:"manual_review_status,omitempty"`
	ReportDir          string  `json:"report_dir"`
}

type KnowledgeSummary struct {
	Passed        bool   `json:"passed"`
	PositiveHits  int    `json:"positive_hits"`
	Relevant      int    `json:"relevant"`
	Returned      int    `json:"returned"`
	NegativeFalse int    `json:"negative_false"`
	Controls      int    `json:"controls_passed"`
	ReportDir     string `json:"report_dir"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("mode", "fixture", "fixture（无模型）| knowledge（仅离线检索）")
	pack := fs.String("pack", "personas/dafeiyu", "人格目录")
	cases := fs.String("cases", "", "人格用例；默认 <pack>/eval/cases.jsonl")
	baseline := fs.String("baseline", "", "基准回复；默认 <pack>/eval/baseline.md")
	badReplies := fs.String("bad-replies", "docs/eval/bad-replies.jsonl", "坏回复用例")
	corpus := fs.String("corpus", "personas/dafeiyu/knowledge", "知识目录")
	knowledgeCases := fs.String("knowledge-cases", "internal/knowledgeeval/testdata/cases.jsonl", "检索用例")
	output := fs.String("output", "", "新建输出目录（必须不存在）")
	strictKnowledge := fs.Bool("strict-knowledge", false, "知识检索未达标时让整轮评测失败")
	if err := fs.Parse(args); err != nil {
		return errors.New("评测参数无效")
	}
	if fs.NArg() != 0 || *output == "" {
		return errors.New("用法：eval --output <新目录> [--mode fixture|knowledge]")
	}
	if *mode != "fixture" && *mode != "knowledge" {
		return errors.New("--mode 只能是 fixture 或 knowledge")
	}
	if *cases == "" {
		*cases = filepath.Join(*pack, "eval", "cases.jsonl")
	}
	if *baseline == "" {
		*baseline = filepath.Join(*pack, "eval", "baseline.md")
	}

	summary := Summary{RunID: time.Now().UTC().Format("20060102T150405Z"), Mode: *mode, StartedAt: time.Now().UTC()}
	if err := os.Mkdir(*output, 0o700); err != nil {
		return fmt.Errorf("必须使用新的输出目录：%w", err)
	}

	if *mode == "fixture" {
		if err := runPersonaFixture(ctx, *output, *pack, *cases, *baseline, &summary); err != nil {
			return err
		}
		if err := runBadReplies(*output, *badReplies, &summary); err != nil {
			return err
		}
	}
	if err := runKnowledge(ctx, *output, *corpus, *knowledgeCases, *strictKnowledge, &summary); err != nil {
		return err
	}

	summary.FinishedAt = time.Now().UTC()
	summary.Passed = len(summary.FailureReasons) == 0
	if err := writeSummary(*output, summary); err != nil {
		return err
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	if !summary.Passed {
		return errors.New("评测未通过，详见 failure_reasons")
	}
	return nil
}

func runPersonaFixture(ctx context.Context, output, pack, cases, baseline string, summary *Summary) error {
	scorer, err := personaeval.NewScorerVersion("persona-lexical-v5")
	if err != nil {
		return err
	}
	fixture, err := personaeval.Prepare(ctx, pack, cases, scorer)
	if err != nil {
		return err
	}
	generator, err := personaeval.NewFixtureGenerator(baseline)
	if err != nil {
		return err
	}
	dir := filepath.Join(output, "persona")
	report, err := personaeval.Run(ctx, fixture, scorer, generator, personaeval.RunOptions{
		RunID:      summary.RunID + "-persona",
		OutputDir:  dir,
		SourceKind: "test",
	})
	if err != nil {
		return err
	}
	summary.Persona = &PersonaSummary{
		SourceKind: report.SourceKind, RulesVersion: report.RulesVersion,
		Total: report.Total, Completed: report.Completed,
		RawPassed: report.RawPassed, DeliveredPassed: report.DeliveredPassed,
		RawPassRate: report.RawPassRate, DeliveredPassRate: report.DeliveredPassRate,
		Complete: report.Complete, PassRateBasis: report.PassRateBasis,
		ManualReviewStatus: report.ManualReviewStatus, ReportDir: dir,
	}
	if !report.Complete {
		summary.FailureReasons = append(summary.FailureReasons, "人格 fixture 未完整跑完")
	}
	return nil
}

func runBadReplies(output, path string, summary *Summary) error {
	cases, err := personaeval.LoadBadReplyCases(path)
	if err != nil {
		return err
	}
	report := personaeval.CheckBadReplies(cases)
	summary.BadReplies = &report
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "bad-replies.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	if !report.Passed {
		summary.FailureReasons = append(summary.FailureReasons, fmt.Sprintf("坏回复检测漏检 %d 条", len(report.Missed)))
	}
	return nil
}

func runKnowledge(ctx context.Context, output, corpus, cases string, strict bool, summary *Summary) error {
	dir := filepath.Join(output, "knowledge")
	report, err := knowledgeeval.RunOffline(ctx, corpus, cases, dir)
	if err != nil {
		return err
	}
	summary.Knowledge = &KnowledgeSummary{
		Passed: report.Passed, PositiveHits: report.Report.Metrics.PositiveHits,
		Relevant: report.Report.Metrics.Relevant, Returned: report.Report.Metrics.Returned,
		NegativeFalse: report.Report.Metrics.NegativeFalse, Controls: report.Report.Metrics.ControlsPassed,
		ReportDir: dir,
	}
	if !report.Passed {
		// K0-7 正例覆盖仍是已知未达标项（见 plans/README.md）；默认只作基线记录，
		// 显式 --strict-knowledge 才让整轮失败，避免用「假装通过」掩盖真实差距。
		if strict {
			summary.FailureReasons = append(summary.FailureReasons, "知识检索离线评测未通过")
		} else {
			summary.Notes = append(summary.Notes, "知识检索离线评测未达标（已知 K0-7 差距，默认不阻塞）")
		}
	}
	return nil
}

func writeSummary(output string, summary Summary) error {
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "summary.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	lines := []string{"# 离线评测报告", "", fmt.Sprintf("- 运行：%s（%s）", summary.RunID, summary.Mode)}
	if p := summary.Persona; p != nil {
		lines = append(lines, fmt.Sprintf("- 人格 fixture：%d/%d 完成，原始通过 %d（%.1f%%），投递通过 %d（%.1f%%），基准=%s",
			p.Completed, p.Total, p.RawPassed, p.RawPassRate*100, p.DeliveredPassed, p.DeliveredPassRate*100, p.PassRateBasis))
	}
	if b := summary.BadReplies; b != nil {
		lines = append(lines, fmt.Sprintf("- 坏回复检测：%d/%d 检出", b.Detected, b.Total))
	}
	if k := summary.Knowledge; k != nil {
		lines = append(lines, fmt.Sprintf("- 知识检索：通过=%t，正例命中 %d，相关 %d/%d，负例误返 %d，镜像控制 %d",
			k.Passed, k.PositiveHits, k.Relevant, k.Returned, k.NegativeFalse, k.Controls))
	}
	if len(summary.FailureReasons) > 0 {
		lines = append(lines, "", "## 未通过原因", "")
		for _, reason := range summary.FailureReasons {
			lines = append(lines, "- "+reason)
		}
	}
	if len(summary.Notes) > 0 {
		lines = append(lines, "", "## 基线说明", "")
		for _, note := range summary.Notes {
			lines = append(lines, "- "+note)
		}
	}
	return os.WriteFile(filepath.Join(output, "summary.md"), []byte(joinLines(lines)), 0o600)
}

func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}
