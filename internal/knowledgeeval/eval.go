// Package knowledgeeval 提供独立于人格回归集的离线检索评测，不创建HTTP或数据库客户端。
package knowledgeeval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"RaiseMyFish/internal/knowledge"
)

// QueryCase 的答案仅供结果评分，Reader和Brain只接收Query与Scope。
type QueryCase struct {
	ID           string           `json:"id"`
	Kind         string           `json:"kind"`
	Form         string           `json:"form"`
	Query        string           `json:"query"`
	Scope        knowledge.Scope  `json:"scope"`
	ExpectedIDs  []string         `json:"expected_ids"`
	Labels       []string         `json:"labels"`
	Rationale    string           `json:"rationale"`
	ControlScope *knowledge.Scope `json:"control_scope,omitempty"`
	BlockedIDs   []string         `json:"blocked_ids,omitempty"`
}

// ReadCases 严格读取JSONL，并检查冻结集必须为60/20/20且ID不重复。
func ReadCases(r io.Reader) ([]QueryCase, error) {
	scanner := bufio.NewScanner(io.LimitReader(r, 2<<20))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var cases []QueryCase
	seen := map[string]bool{}
	counts := map[string]int{}
	for scanner.Scan() {
		var c QueryCase
		d := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil {
			return nil, fmt.Errorf("解析检索用例行%d：%w", len(cases)+1, err)
		}
		var extra any
		if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, errors.New("检索用例含额外JSON")
		}
		if c.ID == "" || seen[c.ID] || c.Query == "" || c.Rationale == "" || len(c.Labels) == 0 {
			return nil, errors.New("检索用例基本字段/ID无效")
		}
		if err := knowledge.ValidateScope(c.Scope); err != nil {
			return nil, fmt.Errorf("检索用例范围：%w", err)
		}
		switch c.Kind {
		case "positive":
			if len(c.ExpectedIDs) == 0 || (c.Form != "exact_title" && c.Form != "natural_title" && c.Form != "natural_paraphrase" && c.Form != "alias") {
				return nil, errors.New("正例缺预期或形式无效")
			}
		case "negative":
			if len(c.ExpectedIDs) != 0 {
				return nil, errors.New("负例不能带相关预期")
			}
		case "permission":
			if len(c.ExpectedIDs) != 0 || len(c.BlockedIDs) == 0 || c.ControlScope == nil || *c.ControlScope == c.Scope {
				return nil, errors.New("权限反例缺独立镜像控制")
			}
			if err := knowledge.ValidateScope(*c.ControlScope); err != nil {
				return nil, fmt.Errorf("镜像范围：%w", err)
			}
		default:
			return nil, errors.New("检索用例类型无效")
		}
		ids := map[string]bool{}
		for _, id := range c.ExpectedIDs {
			if id == "" || ids[id] {
				return nil, errors.New("相关标注ID无效/重复")
			}
			ids[id] = true
		}
		seen[c.ID] = true
		counts[c.Kind]++
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取检索用例：%w", err)
	}
	if len(cases) != 100 || counts["positive"] != 60 || counts["negative"] != 20 || counts["permission"] != 20 {
		return nil, errors.New("冻结集必须为60正例/20负例/20权限反例")
	}
	return cases, nil
}

type CaseResult struct {
	ID               string           `json:"id"`
	Kind             string           `json:"kind"`
	Form             string           `json:"form"`
	Labels           []string         `json:"labels"`
	Scope            knowledge.Scope  `json:"scope"`
	Revision         string           `json:"revision"`
	ExpectedIDs      []string         `json:"expected_ids"`
	ActualIDs        []string         `json:"actual_ids"`
	Relevant         int              `json:"relevant"`
	PositiveHit      bool             `json:"positive_hit"`
	NegativeFalse    bool             `json:"negative_false"`
	ForbiddenReturns int              `json:"forbidden_returns"`
	ControlScope     *knowledge.Scope `json:"control_scope,omitempty"`
	ControlIDs       []string         `json:"control_ids,omitempty"`
	ControlPassed    bool             `json:"control_passed,omitempty"`
	Budget           *BudgetAudit     `json:"budget,omitempty"`
}
type Metrics struct {
	Returned         int            `json:"returned"`
	Relevant         int            `json:"relevant"`
	Precision        *float64       `json:"precision"`
	PositiveHits     int            `json:"positive_hits"`
	NegativeFalse    int            `json:"negative_false"`
	ForbiddenReturns int            `json:"forbidden_returns"`
	ControlsPassed   int            `json:"controls_passed"`
	BudgetPassed     int            `json:"budget_passed"`
	Distribution     map[string]int `json:"distribution"`
	Forms            map[string]int `json:"positive_forms"`
}
type Report struct {
	Cases   []CaseResult `json:"cases"`
	Metrics Metrics      `json:"metrics"`
	Passed  bool         `json:"quality_passed"`
}

// Evaluator 只依赖Reader；质量集与镜像控制分开计数，控制不抬高相关率。
type Evaluator struct {
	Public    knowledge.Reader
	Synthetic knowledge.Reader
	Budget    bool
}

func recall(ctx context.Context, r knowledge.Reader, q knowledge.Query) (knowledge.Context, error) {
	if r == nil {
		return knowledge.Context{}, errors.New("评测Reader不能为空")
	}
	k, err := r.Recall(ctx, q)
	if err != nil {
		return knowledge.Context{}, fmt.Errorf("检索失败：%w", err)
	}
	if k.Scope != q.Scope || len(k.Items) > 3 {
		return knowledge.Context{}, errors.New("检索范围或Top3越界")
	}
	if err := k.Validate(); err != nil {
		return knowledge.Context{}, fmt.Errorf("检索Context无效：%w", err)
	}
	return k, nil
}

func (e Evaluator) Run(ctx context.Context, cases []QueryCase) (Report, error) {
	out := Report{Metrics: Metrics{Distribution: map[string]int{}, Forms: map[string]int{}}}
	if ctx == nil {
		return out, errors.New("评测ctx不能为空")
	}
	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		r := e.Public
		if c.Kind == "permission" {
			r = e.Synthetic
		}
		q := knowledge.Query{Scope: c.Scope, Text: c.Query}
		k, err := recall(ctx, r, q)
		if err != nil {
			return out, fmt.Errorf("用例%s：%w", c.ID, err)
		}
		row := CaseResult{ID: c.ID, Kind: c.Kind, Form: c.Form, Labels: c.Labels, Scope: c.Scope, Revision: k.Revision, ExpectedIDs: c.ExpectedIDs, ActualIDs: []string{}}
		for _, item := range k.Items {
			row.ActualIDs = append(row.ActualIDs, item.ID)
			if slices.Contains(c.ExpectedIDs, item.ID) {
				row.Relevant++
			}
			if c.Kind == "permission" && (item.Private || slices.Contains(c.BlockedIDs, item.ID)) {
				row.ForbiddenReturns++
			}
		}
		row.PositiveHit = c.Kind == "positive" && row.Relevant > 0
		row.NegativeFalse = c.Kind == "negative" && len(k.Items) > 0
		if c.Kind == "permission" {
			if c.ControlScope == nil {
				return out, errors.New("权限用例缺镜像控制")
			}
			control, err := recall(ctx, r, knowledge.Query{Scope: *c.ControlScope, Text: c.Query})
			if err != nil {
				return out, fmt.Errorf("镜像%s：%w", c.ID, err)
			}
			row.ControlScope = c.ControlScope
			for _, item := range control.Items {
				row.ControlIDs = append(row.ControlIDs, item.ID)
			}
			row.ControlPassed = true
			for _, id := range c.BlockedIDs {
				if !slices.Contains(row.ControlIDs, id) {
					row.ControlPassed = false
				}
			}
			if row.ControlPassed {
				out.Metrics.ControlsPassed++
			}
		}
		if e.Budget {
			audit, err := CheckBudget(ctx, q, k)
			if err != nil {
				return out, fmt.Errorf("预算%s：%w", c.ID, err)
			}
			row.Budget = &audit
			if audit.Passed {
				out.Metrics.BudgetPassed++
			}
		}
		out.Metrics.Returned += len(k.Items)
		out.Metrics.Relevant += row.Relevant
		if row.PositiveHit {
			out.Metrics.PositiveHits++
		}
		if row.NegativeFalse {
			out.Metrics.NegativeFalse++
		}
		out.Metrics.ForbiddenReturns += row.ForbiddenReturns
		out.Metrics.Distribution[c.Kind]++
		if c.Kind == "positive" {
			out.Metrics.Forms[c.Form]++
		}
		out.Cases = append(out.Cases, row)
	}
	m := &out.Metrics
	if m.Returned > 0 {
		v := float64(m.Relevant) / float64(m.Returned)
		m.Precision = &v
	}
	out.Passed = len(cases) == 100 && m.Distribution["positive"] == 60 && m.Distribution["negative"] == 20 && m.Distribution["permission"] == 20 && m.Precision != nil && *m.Precision >= .8 && m.PositiveHits >= 48 && m.NegativeFalse <= 2 && m.ForbiddenReturns == 0 && m.ControlsPassed == 20 && (!e.Budget || m.BudgetPassed == 100)
	return out, nil
}
