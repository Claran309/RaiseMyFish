package personaeval

// 本文件定义 Reflection 对照集与评测协议（R3/R7 的仓库侧部分）。
//
// 四类用例：该改 / 不该改 / 预算边界 / 安全边界。
// 指标：improve_rate/harm_rate/changed_rate/skip_rate；没有人工或 judge 标注时
// 明确标 Pending，不把「未评估」当 0 分。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ReflectionCase 是一条对照用例；ExpectReflect 是人工先验分类。
type ReflectionCase struct {
	ID            string `json:"id"`
	Channel       string `json:"channel"`
	Input         string `json:"input"`
	ExpectReflect string `json:"expect_reflect"`
	Problem       string `json:"problem"`
}

// ReflectionRun 是一条录制结果；Verdict 由人工或 LLM judge 标注。
type ReflectionRun struct {
	CaseID  string `json:"case_id"`
	Reflect bool   `json:"reflect"`
	Changed bool   `json:"changed"`
	Skipped string `json:"skipped_reason,omitempty"`
	Verdict string `json:"verdict,omitempty"`
}

// ReflectionReport 是对照集汇总。
type ReflectionReport struct {
	Cases        int      `json:"cases"`
	Evaluated    int      `json:"evaluated"`
	Skipped      []string `json:"skipped,omitempty"`
	ChangedRate  float64  `json:"changed_rate"`
	ImproveRate  float64  `json:"improve_rate"`
	HarmRate     float64  `json:"harm_rate"`
	SkipRate     float64  `json:"skip_rate"`
	HarmAlarm    bool     `json:"harm_alarm"`
	JudgePending bool     `json:"judge_pending"`
}

// LoadReflectionCases 读取 50 条对照用例。
func LoadReflectionCases(path string) ([]ReflectionCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取反思用例失败：%w", err)
	}
	defer file.Close()
	var cases []ReflectionCase
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		var c ReflectionCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("反思用例第%d行无效：%w", len(cases)+1, err)
		}
		if c.ID == "" || c.Input == "" || c.ExpectReflect == "" {
			return nil, fmt.Errorf("反思用例第%d行缺字段", len(cases)+1)
		}
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("反思用例为空：%s", path)
	}
	return cases, nil
}

// ScoreReflection 计算对照指标；没有录制的用例标 skip，有录制但无 verdict 的标 Pending。
func ScoreReflection(cases []ReflectionCase, runs map[string]ReflectionRun) ReflectionReport {
	report := ReflectionReport{Cases: len(cases)}
	var called, changed, improved, harmed, skipped int
	var judged int
	for _, c := range cases {
		run, ok := runs[c.ID]
		if !ok {
			report.Skipped = append(report.Skipped, c.ID+": 缺少录制结果")
			skipped++
			continue
		}
		report.Evaluated++
		if !run.Reflect || run.Skipped != "" {
			skipped++
			continue
		}
		called++
		if run.Changed {
			changed++
		}
		switch run.Verdict {
		case "improved":
			improved++
			judged++
		case "harmed":
			harmed++
			judged++
		case "":
			// 未标注：不参与 improve/harm 分母。
		default:
			judged++
		}
	}
	if report.Evaluated > 0 {
		report.SkipRate = float64(skipped) / float64(report.Evaluated)
	}
	if called > 0 {
		report.ChangedRate = float64(changed) / float64(called)
	}
	if judged > 0 {
		report.ImproveRate = float64(improved) / float64(judged)
		report.HarmRate = float64(harmed) / float64(judged)
	}
	report.JudgePending = judged == 0
	report.HarmAlarm = judged > 0 && report.HarmRate > 0.05
	SortSkipped(report.Skipped)
	return report
}
