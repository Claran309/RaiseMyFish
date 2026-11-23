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
	"strings"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/conversationeval"
	"RaiseMyFish/internal/knowledgeeval"
	"RaiseMyFish/internal/memoryeval"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/personaeval"
)

// Summary 是一次评测的总览；字段全部可由本文件重新计算。
type Summary struct {
	RunID          string                        `json:"run_id"`
	Mode           string                        `json:"mode"`
	StartedAt      time.Time                     `json:"started_at"`
	FinishedAt     time.Time                     `json:"finished_at"`
	Persona        *PersonaSummary               `json:"persona,omitempty"`
	BadReplies     *personaeval.BadReplyReport   `json:"bad_replies,omitempty"`
	Knowledge      *KnowledgeSummary             `json:"knowledge,omitempty"`
	Trajectory     *personaeval.TrajectoryReport `json:"trajectory,omitempty"`
	Reflection     *personaeval.ReflectionReport `json:"reflection,omitempty"`
	Conversation   *conversationeval.Report      `json:"conversation,omitempty"`
	Memory         *memoryeval.Report            `json:"memory,omitempty"`
	Passed         bool                          `json:"passed"`
	FailureReasons []string                      `json:"failure_reasons,omitempty"`
	Notes          []string                      `json:"notes,omitempty"`
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
	trajectoryCases := fs.String("trajectory-cases", "", "轨迹用例；默认 <pack>/eval/trajectory-cases.jsonl")
	trajectoryRuns := fs.String("trajectory-runs", "", "可选：录制好的轨迹 JSONL（真实 ReAct 运行）")
	reflectionCases := fs.String("reflection-cases", "", "反思对照用例；默认 <pack>/eval/reflection-cases.jsonl")
	reflectionRuns := fs.String("reflection-runs", "", "可选：录制好的反思结果 JSONL（含 verdict）")
	conversationCases := fs.String("conversation-cases", "", "会话多轮用例；默认 <pack>/eval/conversation-cases.jsonl")
	recentTurns := fs.Int("conversation-recent-turns", 8, "会话评测注入的最近轮数（与线上配置一致）")
	promptTokens := fs.Int("conversation-max-prompt-tokens", 65536, "会话评测的 prompt 预算（与线上 max_prompt_tokens 一致）")
	memoryCases := fs.String("memory-cases", "", "记忆评测用例；默认 <pack>/eval/memory-cases.jsonl")
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
	if *trajectoryCases == "" {
		*trajectoryCases = filepath.Join(*pack, "eval", "trajectory-cases.jsonl")
	}
	if *reflectionCases == "" {
		*reflectionCases = filepath.Join(*pack, "eval", "reflection-cases.jsonl")
	}
	if *conversationCases == "" {
		*conversationCases = filepath.Join(*pack, "eval", "conversation-cases.jsonl")
	}
	if *memoryCases == "" {
		*memoryCases = filepath.Join(*pack, "eval", "memory-cases.jsonl")
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
		if err := runTrajectory(*output, *trajectoryCases, *trajectoryRuns, &summary); err != nil {
			return err
		}
		if err := runReflection(*output, *reflectionCases, *reflectionRuns, &summary); err != nil {
			return err
		}
		if err := runConversation(ctx, *output, *pack, *conversationCases, *recentTurns, *promptTokens, &summary); err != nil {
			return err
		}
		if err := runMemoryEval(ctx, *output, *memoryCases, &summary); err != nil {
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

// runTrajectory 校验轨迹用例并记录样本情况。
//
// fixture 回放没有 ReAct 轨迹，因此默认全部 skip；指标只有在真实 ReAct 评测
// 提供轨迹时才计算，绝不把「没有轨迹」当成 0 分通过率。
func runTrajectory(output, path, runsPath string, summary *Summary) error {
	cases, err := personaeval.LoadTrajectoryCases(path)
	if err != nil {
		return err
	}
	if err := personaeval.ValidateTrajectoryCases(cases, personaeval.TrajectoryToolNames); err != nil {
		return err
	}
	var report personaeval.TrajectoryReport
	if runsPath != "" {
		runs, err := personaeval.LoadTrajectoryRuns(runsPath)
		if err != nil {
			return err
		}
		report = personaeval.ScoreTrajectory(cases, runs)
	} else {
		report = personaeval.TrajectoryReport{Cases: len(cases)}
		for _, c := range cases {
			report.Skipped = append(report.Skipped, c.ID+": 缺少轨迹（未走 ReAct）")
		}
		personaeval.SortSkipped(report.Skipped)
	}
	summary.Trajectory = &report
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "trajectory.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	if report.Evaluated == 0 {
		summary.Notes = append(summary.Notes, fmt.Sprintf("轨迹用例 %d 条已校验；fixture 回放无 ReAct 轨迹，全部标 skip", len(cases)))
	} else {
		summary.Notes = append(summary.Notes, fmt.Sprintf("轨迹用例 %d 条，已评估 %d 条，skip %d 条", len(cases), report.Evaluated, len(report.Skipped)))
	}
	return nil
}

// runReflection 校验 50 条对照用例；没有录制结果时全部标 skip，不假装有指标。
func runReflection(output, path, runsPath string, summary *Summary) error {
	cases, err := personaeval.LoadReflectionCases(path)
	if err != nil {
		return err
	}
	runs := map[string]personaeval.ReflectionRun{}
	if runsPath != "" {
		loaded, err := loadReflectionRuns(runsPath)
		if err != nil {
			return err
		}
		runs = loaded
	}
	report := personaeval.ScoreReflection(cases, runs)
	summary.Reflection = &report
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "reflection.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	if report.Evaluated == 0 {
		summary.Notes = append(summary.Notes, fmt.Sprintf("反思用例 %d 条已校验；未提供录制结果，全部标 skip", len(cases)))
	} else {
		summary.Notes = append(summary.Notes, fmt.Sprintf("反思对照 %d 条，已评估 %d 条，skip %d 条，judge_pending=%t",
			report.Cases, report.Evaluated, len(report.Skipped), report.JudgePending))
	}
	if report.HarmAlarm {
		summary.Notes = append(summary.Notes, "反思伤害率超过 5%，建议关闭评论/私信反思或调整 critic")
	}
	return nil
}

// runMemoryEval 跑 100 条记忆用例：规则提炼、写入闸门与版本键。
func runMemoryEval(ctx context.Context, output, path string, summary *Summary) error {
	cases, err := memoryeval.LoadCases(path)
	if err != nil {
		return err
	}
	report, err := memoryeval.Run(ctx, cases)
	if err != nil {
		return err
	}
	summary.Memory = &report
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "memory.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	summary.Notes = append(summary.Notes, fmt.Sprintf(
		"记忆用例 %d 条：规则召回 %.1f%%，误报 %d，隐私泄漏 %d，闸门准确率 %.1f%%，版本键 %.1f%%",
		report.Cases, report.Recall*100, report.FalsePositive, report.PrivacyLeak,
		report.GateAccuracy*100, report.KeyAccuracy*100))
	if !report.Passed {
		summary.FailureReasons = append(summary.FailureReasons, "记忆评测未达标")
	}
	return nil
}

// runConversation 跑会话多轮离线评测：用真实人格包装配 prompt，量追问可达性。
//
// 它不调模型，所以「追问解决率」只表示先行词进入了 prompt；
// 延迟与成本保持 null，由真实模型评测单独补。
func runConversation(ctx context.Context, output, packDir, path string, recent, promptTokens int, summary *Summary) error {
	cases, err := conversationeval.LoadCases(path)
	if err != nil {
		return err
	}
	pack, err := persona.Load(ctx, packDir)
	if err != nil {
		return err
	}
	budget := brain.DefaultBudget()
	if promptTokens > 0 {
		budget.MaxPromptTokens = promptTokens
	}
	report, err := conversationeval.Run(ctx, cases, conversationeval.Options{
		Pack: pack, RecentTurns: recent, Budget: budget,
	})
	if err != nil {
		return err
	}
	summary.Conversation = &report
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "conversation.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	summary.Notes = append(summary.Notes, fmt.Sprintf(
		"会话用例 %d 条：追问解决率 %.1f%%（基线 %.1f%%），串人率 %.1f%%，token 变化 %.1f%%",
		report.Cases, report.FollowupResolutionRate*100, report.BaselineResolutionRate*100,
		report.CrossUserLeakRate*100, report.PromptTokenDeltaPct))
	// 验收线：串人率必须为 0；提升不足 5 个百分点说明会话段没有价值。
	if report.CrossUserLeakRate > 0 {
		summary.FailureReasons = append(summary.FailureReasons, "会话评测出现串人/键冲突")
	}
	if report.DeltaPoints < 5 {
		summary.FailureReasons = append(summary.FailureReasons, "会话追问提升不足 5 个百分点")
	}
	return nil
}

func loadReflectionRuns(path string) (map[string]personaeval.ReflectionRun, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取反思录制失败：%w", err)
	}
	runs := map[string]personaeval.ReflectionRun{}
	for i, line := range splitLines(string(data)) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var run personaeval.ReflectionRun
		if err := json.Unmarshal([]byte(line), &run); err != nil {
			return nil, fmt.Errorf("反思录制第%d行无效：%w", i+1, err)
		}
		if run.CaseID == "" {
			return nil, fmt.Errorf("反思录制第%d行缺 case_id", i+1)
		}
		runs[run.CaseID] = run
	}
	return runs, nil
}

func splitLines(text string) []string {
	var out []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			out = append(out, text[start:i])
			start = i + 1
		}
	}
	out = append(out, text[start:])
	return out
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
	if t := summary.Trajectory; t != nil {
		lines = append(lines, fmt.Sprintf("- 轨迹用例：%d 条，已评估 %d，skip %d；工具选择准确率 %.2f，不必要工具率 %.2f，平均步数 %.2f，证据覆盖 %.2f，幻觉率 %.2f",
			t.Cases, t.Evaluated, len(t.Skipped), t.ToolSelectionAccuracy.Value, t.UnnecessaryToolRate.Value,
			t.AvgSteps.Value, t.Groundedness.Value, t.HallucinationRate.Value))
	}
	if r := summary.Reflection; r != nil {
		lines = append(lines, fmt.Sprintf("- 反思对照：%d 条，已评估 %d，skip %d；changed %.2f，improve %.2f，harm %.2f，judge_pending=%t",
			r.Cases, r.Evaluated, len(r.Skipped), r.ChangedRate, r.ImproveRate, r.HarmRate, r.JudgePending))
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
