package personaeval

// 本文件定义轨迹级评测：工具序列、步数、证据覆盖与五项指标。
//
// 边界：
//   - 轨迹只用于评测，不进入模型输入（Input 不含 ExpectedTools）；
//   - 指标是纯函数，可在 mock/真实模型两侧复用；
//   - 没有轨迹的用例明确记 skip 原因，不按 0 分混进分母。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ToolCallRecord 是一次工具调用的审计投影；ArgsDigest 只存摘要，不存完整参数。
type ToolCallRecord struct {
	Name       string `json:"name"`
	ArgsDigest string `json:"args_digest,omitempty"`
	OK         bool   `json:"ok"`
	MS         int    `json:"ms,omitempty"`
}

// Trajectory 是一次生成的执行轨迹。
type Trajectory struct {
	Steps         int              `json:"steps"`
	Tools         []ToolCallRecord `json:"tools,omitempty"`
	EvidenceCount int              `json:"evidence_count"`
	Degraded      bool             `json:"degraded"`
	Reason        string           `json:"reason,omitempty"`
}

// TrajectoryMetric 是单个指标的统计结果。
type TrajectoryMetric struct {
	Value       float64 `json:"value"`
	Numerator   int     `json:"numerator"`
	Denominator int     `json:"denominator"`
}

// TrajectoryReport 是轨迹评测汇总；Skipped 说明为何没有样本。
type TrajectoryReport struct {
	Cases                 int              `json:"cases"`
	Evaluated             int              `json:"evaluated"`
	Skipped               []string         `json:"skipped,omitempty"`
	ToolSelectionAccuracy TrajectoryMetric `json:"tool_selection_accuracy"`
	UnnecessaryToolRate   TrajectoryMetric `json:"unnecessary_tool_rate"`
	AvgSteps              TrajectoryMetric `json:"avg_steps"`
	Groundedness          TrajectoryMetric `json:"groundedness"`
	HallucinationRate     TrajectoryMetric `json:"hallucination_rate"`
}

// TrajectoryCase 是「该调什么工具」用例；只给评测器，不传给模型。
type TrajectoryCase struct {
	ID             string   `json:"id"`
	Channel        string   `json:"channel"`
	Input          string   `json:"input"`
	ExpectedTools  []string `json:"expected_tools"`
	ForbiddenTools []string `json:"forbidden_tools,omitempty"`
	// ShouldUseTools=false 表示纯寒暄，不应调用任何工具。
	ShouldUseTools bool   `json:"should_use_tools"`
	Rationale      string `json:"rationale"`
}

// CheckTrajectory 计算单条用例的轨迹指标；无轨迹时返回 skip 原因。
func CheckTrajectory(c TrajectoryCase, t *Trajectory) (TrajectoryReport, string) {
	if t == nil {
		return TrajectoryReport{}, "缺少轨迹（未走 ReAct）"
	}
	report := TrajectoryReport{Cases: 1, Evaluated: 1}
	used := make(map[string]bool, len(t.Tools))
	for _, tool := range t.Tools {
		used[tool.Name] = true
	}
	expectedHit := 0
	for _, name := range c.ExpectedTools {
		if used[name] {
			expectedHit++
		}
	}
	forbiddenHit := 0
	for _, name := range c.ForbiddenTools {
		if used[name] {
			forbiddenHit++
		}
	}
	unnecessary := 0
	for name := range used {
		if !containsTool(c.ExpectedTools, name) {
			unnecessary++
		}
	}
	if !c.ShouldUseTools && len(t.Tools) > 0 {
		unnecessary = len(t.Tools)
	}
	report.ToolSelectionAccuracy = metric(expectedHit, len(c.ExpectedTools))
	report.UnnecessaryToolRate = metric(unnecessary, max(1, len(t.Tools)))
	report.AvgSteps = metric(t.Steps, 1)
	report.Groundedness = metric(t.EvidenceCount, max(1, len(t.Tools)))
	// 幻觉率按「用例」计：该用例是否踩了禁用工具；没有禁用清单时不计入分母。
	if len(c.ForbiddenTools) > 0 {
		report.HallucinationRate = metric(minInt(1, forbiddenHit), 1)
	}
	return report, ""
}

// MergeTrajectory 把多条用例结果合成一份报告。
func MergeTrajectory(reports []TrajectoryReport, skipped []string) TrajectoryReport {
	out := TrajectoryReport{Cases: len(reports) + len(skipped), Skipped: append([]string(nil), skipped...)}
	var selectionNum, selectionDen, unnecessaryNum, unnecessaryDen, steps, groundedNum, groundedDen, hallNum, hallDen int
	for _, r := range reports {
		if r.Evaluated == 0 {
			continue
		}
		out.Evaluated++
		selectionNum += r.ToolSelectionAccuracy.Numerator
		selectionDen += r.ToolSelectionAccuracy.Denominator
		unnecessaryNum += r.UnnecessaryToolRate.Numerator
		unnecessaryDen += r.UnnecessaryToolRate.Denominator
		steps += int(r.AvgSteps.Value)
		groundedNum += r.Groundedness.Numerator
		groundedDen += r.Groundedness.Denominator
		hallNum += r.HallucinationRate.Numerator
		hallDen += r.HallucinationRate.Denominator
	}
	out.ToolSelectionAccuracy = metric(selectionNum, selectionDen)
	out.UnnecessaryToolRate = metric(unnecessaryNum, unnecessaryDen)
	if out.Evaluated > 0 {
		out.AvgSteps = metric(steps, out.Evaluated)
	}
	out.Groundedness = metric(groundedNum, groundedDen)
	out.HallucinationRate = metric(hallNum, hallDen)
	return out
}

// SortSkipped 让 skip 列表稳定，便于报告 diff。
func SortSkipped(skipped []string) {
	sort.Strings(skipped)
}

func metric(num, den int) TrajectoryMetric {
	m := TrajectoryMetric{Numerator: num, Denominator: den}
	if den > 0 {
		m.Value = float64(num) / float64(den)
	}
	return m
}

func containsTool(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ValidateTrajectoryCases 在跑分前拒绝空用例、重复 ID 与非法工具名。
func ValidateTrajectoryCases(cases []TrajectoryCase, allowedTools []string) error {
	if len(cases) == 0 {
		return fmt.Errorf("轨迹用例为空")
	}
	allowed := make(map[string]bool, len(allowedTools))
	for _, name := range allowedTools {
		allowed[name] = true
	}
	seen := make(map[string]bool, len(cases))
	for _, c := range cases {
		if strings.TrimSpace(c.ID) == "" || seen[c.ID] || strings.TrimSpace(c.Input) == "" {
			return fmt.Errorf("轨迹用例 ID/输入无效或重复：%s", c.ID)
		}
		seen[c.ID] = true
		for _, name := range append(append([]string(nil), c.ExpectedTools...), c.ForbiddenTools...) {
			if !allowed[name] {
				return fmt.Errorf("轨迹用例 %s 引用未注册工具 %s", c.ID, name)
			}
		}
		if c.ShouldUseTools && len(c.ExpectedTools) == 0 {
			return fmt.Errorf("轨迹用例 %s 声明需要工具但没有期望工具", c.ID)
		}
	}
	return nil
}

// LoadTrajectoryCases 严格读取轨迹用例 JSONL。
func LoadTrajectoryCases(path string) ([]TrajectoryCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取轨迹用例失败：%w", err)
	}
	defer file.Close()
	var cases []TrajectoryCase
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		var c TrajectoryCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("轨迹用例第%d行无效：%w", len(cases)+1, err)
		}
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取轨迹用例：%w", err)
	}
	return cases, nil
}

// TrajectoryRun 是一条「录制好的」真实运行轨迹，用于离线对比。
//
// 录制来源可以是 ReAct 评测或线上抽样；只含工具名/步数/证据数，不含正文。
type TrajectoryRun struct {
	CaseID     string      `json:"case_id"`
	RunID      string      `json:"run_id,omitempty"`
	Trajectory *Trajectory `json:"trajectory"`
}

// LoadTrajectoryRuns 读取录制轨迹 JSONL；同一 case 多条时后者覆盖前者。
func LoadTrajectoryRuns(path string) (map[string]*Trajectory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取录制轨迹失败：%w", err)
	}
	defer file.Close()
	runs := map[string]*Trajectory{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		var run TrajectoryRun
		if err := json.Unmarshal([]byte(line), &run); err != nil {
			return nil, fmt.Errorf("录制轨迹行无效：%w", err)
		}
		if strings.TrimSpace(run.CaseID) == "" || run.Trajectory == nil {
			return nil, fmt.Errorf("录制轨迹缺 case_id 或 trajectory")
		}
		runs[run.CaseID] = run.Trajectory
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取录制轨迹：%w", err)
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("录制轨迹为空：%s", path)
	}
	return runs, nil
}

// ScoreTrajectory 用录制轨迹给用例打分；没有录制轨迹的用例记 skip。
func ScoreTrajectory(cases []TrajectoryCase, runs map[string]*Trajectory) TrajectoryReport {
	var reports []TrajectoryReport
	var skipped []string
	for _, c := range cases {
		report, skip := CheckTrajectory(c, runs[c.ID])
		if skip != "" {
			skipped = append(skipped, c.ID+": "+skip)
			continue
		}
		reports = append(reports, report)
	}
	SortSkipped(skipped)
	return MergeTrajectory(reports, skipped)
}

// TrajectoryToolNames 是评测允许引用的工具名；与 reasoning 工具常量保持一致。
var TrajectoryToolNames = []string{
	"memory_search", "knowledge_search", "presence", "history",
	"viewer_memory", "viewer_search", "reply_history", "live_status",
	"quota", "now", "vision", "web_search", "watch_live", "send_danmaku",
	"gift", "follow", "like", "dm", "post_dynamic",
}
