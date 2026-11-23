// Package memoryeval 是长期记忆的离线评测。
//
// 口径分三层，全部用生产同一份实现，不另写评分规则：
//  1. 规则提炼：承诺/主人相关/首次出现是否被正确提取（memory.Extract）；
//  2. 写入闸门：偏好是否有稳定信号（memory.ClassifyValue）；
//  3. 版本键：同主体同谓词必须同键，不同主体不得互相覆盖（memory.FactKey）。
//
// 边界：不调用模型；「该记住的事实」在规则版里本来就不产 fact，
// 评测只声明规则版能力，不把 LLM 能力算成规则版的功劳。
package memoryeval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

// Case 是一条记忆评测用例。
type Case struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
	// 规则提炼相关。
	ExpectKind string `json:"expect_kind,omitempty"`
	Private    bool   `json:"private,omitempty"`
	AllowPravt bool   `json:"allow_private,omitempty"`
	FromID     string `json:"from_id,omitempty"`
	FromName   string `json:"from_name,omitempty"`
	OwnerUID   string `json:"owner_uid,omitempty"`
	FirstNick  bool   `json:"first_nick,omitempty"`
	// 写入闸门相关。
	ExpectGate bool `json:"expect_gate,omitempty"`
	// 版本键相关。
	Subject string `json:"subject,omitempty"`
	Title   string `json:"title,omitempty"`
	Title2  string `json:"title2,omitempty"`
	SameKey bool   `json:"same_key,omitempty"`
}

// Result 是单条用例结果。
type Result struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason,omitempty"`
}

// Report 是评测汇总。
type Report struct {
	Cases int `json:"cases"`
	// 规则提炼：该记住的命中 / 不该记住的误报。
	RememberTotal int     `json:"remember_total"`
	RememberHit   int     `json:"remember_hit"`
	Recall        float64 `json:"recall"`
	NotTotal      int     `json:"not_remember_total"`
	FalsePositive int     `json:"false_positive"`
	// 隐私：未授权私密事件不得产出候选。
	PrivacyTotal int `json:"privacy_total"`
	PrivacyLeak  int `json:"privacy_leak"`
	// 写入闸门。
	GateTotal    int     `json:"gate_total"`
	GateCorrect  int     `json:"gate_correct"`
	GateAccuracy float64 `json:"gate_accuracy"`
	// 版本键。
	KeyTotal    int      `json:"key_total"`
	KeyCorrect  int      `json:"key_correct"`
	KeyAccuracy float64  `json:"key_accuracy"`
	Passed      bool     `json:"passed"`
	Detail      []Result `json:"detail"`
}

// LoadCases 读取 JSONL 用例。
func LoadCases(path string) ([]Case, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取记忆用例失败：%w", err)
	}
	defer file.Close()
	var cases []Case
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		var c Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("记忆用例第%d行无效：%w", len(cases)+1, err)
		}
		if err := validate(c); err != nil {
			return nil, fmt.Errorf("记忆用例第%d行：%w", len(cases)+1, err)
		}
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return cases, nil
}

func validate(c Case) error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("缺 id")
	}
	switch c.Type {
	case "remember", "not_remember", "privacy", "privacy_allowed", "value_gate", "fact_key":
	default:
		return fmt.Errorf("未知用例类型 %q", c.Type)
	}
	if c.Type != "fact_key" && strings.TrimSpace(c.Text) == "" {
		return fmt.Errorf("缺 text")
	}
	if c.Type == "remember" || c.Type == "privacy_allowed" {
		if c.ExpectKind == "" {
			return fmt.Errorf("缺 expect_kind")
		}
	}
	if c.Type == "fact_key" && (c.Subject == "" || c.Title == "" || c.Title2 == "") {
		return fmt.Errorf("版本键用例缺 subject/title/title2")
	}
	return nil
}

// Run 执行评测。
func Run(ctx context.Context, cases []Case) (Report, error) {
	if ctx == nil {
		return Report{}, fmt.Errorf("ctx 不能为 nil")
	}
	report := Report{Cases: len(cases), Detail: make([]Result, 0, len(cases))}
	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		result := runCase(c)
		report.Detail = append(report.Detail, result)
		switch c.Type {
		case "remember":
			report.RememberTotal++
			if result.Passed {
				report.RememberHit++
			}
		case "not_remember":
			report.NotTotal++
			if !result.Passed {
				report.FalsePositive++
			}
		case "privacy":
			report.PrivacyTotal++
			if !result.Passed {
				report.PrivacyLeak++
			}
		case "value_gate":
			report.GateTotal++
			if result.Passed {
				report.GateCorrect++
			}
		case "fact_key":
			report.KeyTotal++
			if result.Passed {
				report.KeyCorrect++
			}
		}
	}
	if report.RememberTotal > 0 {
		report.Recall = float64(report.RememberHit) / float64(report.RememberTotal)
	}
	if report.GateTotal > 0 {
		report.GateAccuracy = float64(report.GateCorrect) / float64(report.GateTotal)
	}
	if report.KeyTotal > 0 {
		report.KeyAccuracy = float64(report.KeyCorrect) / float64(report.KeyTotal)
	}
	report.Passed = report.Recall >= 0.9 && report.FalsePositive == 0 &&
		report.PrivacyLeak == 0 && report.GateAccuracy >= 0.9 && report.KeyAccuracy == 1
	return report, nil
}

func runCase(c Case) Result {
	result := Result{ID: c.ID, Type: c.Type}
	switch c.Type {
	case "remember", "not_remember", "privacy", "privacy_allowed":
		ev := event.Event{
			ID: "dm:1:2:" + c.ID, Channel: event.ChDM, Dir: event.DirIn, Session: "1:2",
			FromID: c.FromID, FromName: c.FromName, Text: c.Text, Private: c.Private,
		}
		candidates := memory.Extract(ev, memory.ExtractOptions{
			OwnerUID: c.OwnerUID, AllowPrivate: c.AllowPravt, IsFirstNick: c.FirstNick,
		})
		switch c.Type {
		case "remember", "privacy_allowed":
			found := false
			for _, candidate := range candidates {
				if candidate.Kind == c.ExpectKind {
					found = true
					break
				}
			}
			result.Passed = found
			if !found {
				result.Reason = fmt.Sprintf("未产出 %s 候选（实际 %d 条）", c.ExpectKind, len(candidates))
			}
		case "not_remember":
			result.Passed = len(candidates) == 0
			if !result.Passed {
				result.Reason = fmt.Sprintf("噪声产生了 %d 条候选", len(candidates))
			}
		case "privacy":
			result.Passed = len(candidates) == 0
			if !result.Passed {
				result.Reason = fmt.Sprintf("未授权私密事件产出了 %d 条候选", len(candidates))
			}
		}
	case "value_gate":
		decision := memory.ClassifyValue(c.Text)
		result.Passed = decision.Keep == c.ExpectGate
		if !result.Passed {
			result.Reason = fmt.Sprintf("闸门=%v 期望 %v（%s）", decision.Keep, c.ExpectGate, decision.Reason)
		}
	case "fact_key":
		left := memory.FactKey("public", c.Subject, c.Title)
		right := memory.FactKey("public", c.Subject, c.Title2)
		if c.SameKey {
			result.Passed = left != "" && left == right
		} else {
			result.Passed = left != "" && right != "" && left != right
		}
		if !result.Passed {
			result.Reason = fmt.Sprintf("版本键=%q/%q，期望 same=%v", left, right, c.SameKey)
		}
	}
	return result
}
