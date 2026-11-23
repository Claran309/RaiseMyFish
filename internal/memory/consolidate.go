package memory

// 本文件是记忆巩固/遗忘的纯规则：只产出「建议」，不直接写库。
// 默认 dry-run：调用方拿到计划后先出报告，人工确认再执行归档/降权。

import (
	"sort"
	"strings"
	"time"
)

// ConsolidationCandidate 是运维查询到的一条当前有效记忆。
type ConsolidationCandidate struct {
	ID         string
	Kind       string
	Title      string
	Summary    string
	Importance float64
	Confidence float64
	ValidFrom  time.Time
	Archived   bool
	FactKey    string
	// ReferenceCount 是该记录被回复引用的次数（memory_usage 聚合）。
	ReferenceCount int
	// LastReferencedAt 是最近一次被引用时间；零值表示从未引用。
	LastReferencedAt time.Time
}

// ConsolidationAction 是一条建议动作。
type ConsolidationAction struct {
	ID     string `json:"id"`
	Action string `json:"action"` // archive / downweight / supersede
	Reason string `json:"reason"`
}

// ConsolidationPlan 是一次 dry-run 结果。
type ConsolidationPlan struct {
	Scanned     int                   `json:"scanned"`
	Actions     []ConsolidationAction `json:"actions"`
	DryRun      bool                  `json:"dry_run"`
	GeneratedAt time.Time             `json:"generated_at"`
}

// ConsolidateOptions 是巩固阈值；全部有默认值。
type ConsolidateOptions struct {
	Now time.Time
	// DecayWindow 是「从未被引用且低重要度」的归档窗口。
	DecayWindow time.Duration
	// MinImportance 低于该值才可能被归档。
	MinImportance float64
	// MaxConfidence 高于该值不归档（宁可留噪音也不误删可信事实）。
	MaxConfidence float64
}

func (o ConsolidateOptions) resolve() ConsolidateOptions {
	if o.Now.IsZero() {
		o.Now = time.Now().UTC()
	}
	if o.DecayWindow <= 0 {
		o.DecayWindow = 30 * 24 * time.Hour
	}
	if o.MinImportance <= 0 {
		o.MinImportance = 0.5
	}
	if o.MaxConfidence <= 0 {
		o.MaxConfidence = 0.7
	}
	return o
}

// BuildConsolidationPlan 产出归档建议；不修改输入，不写库。
//
// 规则：
//  1. 高重要度（≥0.9）永不自动归档；
//  2. 从未被引用、超过衰减窗口、低重要度且低置信度 → archive；
//  3. 同 fact_key 多条 active（历史脏数据）→ supersede 提示，交人工确认保留哪条；
//  4. 其余不动。
func BuildConsolidationPlan(rows []ConsolidationCandidate, opts ConsolidateOptions) ConsolidationPlan {
	opts = opts.resolve()
	plan := ConsolidationPlan{Scanned: len(rows), DryRun: true, GeneratedAt: opts.Now}
	byFactKey := map[string][]ConsolidationCandidate{}
	for _, row := range rows {
		if row.Archived || strings.TrimSpace(row.ID) == "" {
			continue
		}
		if strings.TrimSpace(row.FactKey) != "" {
			byFactKey[row.FactKey] = append(byFactKey[row.FactKey], row)
		}
		if row.Importance >= 0.9 {
			continue
		}
		if row.ReferenceCount > 0 {
			continue
		}
		if !row.LastReferencedAt.IsZero() && opts.Now.Sub(row.LastReferencedAt) < opts.DecayWindow {
			continue
		}
		if row.Importance >= opts.MinImportance || row.Confidence >= opts.MaxConfidence {
			continue
		}
		plan.Actions = append(plan.Actions, ConsolidationAction{ID: row.ID, Action: "archive", Reason: "low_value_never_referenced"})
	}
	for key, group := range byFactKey {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].ValidFrom.After(group[j].ValidFrom) })
		for _, stale := range group[1:] {
			plan.Actions = append(plan.Actions, ConsolidationAction{ID: stale.ID, Action: "supersede", Reason: "duplicate_fact_key:" + key})
		}
	}
	sort.Slice(plan.Actions, func(i, j int) bool {
		if plan.Actions[i].ID == plan.Actions[j].ID {
			return plan.Actions[i].Action < plan.Actions[j].Action
		}
		return plan.Actions[i].ID < plan.Actions[j].ID
	})
	return plan
}

// DecayScore 是召回排序用的时间衰减分；高重要度不衰减。
func DecayScore(importance float64, lastReferenced time.Time, referenceCount int, now time.Time) float64 {
	if importance >= 0.9 || lastReferenced.IsZero() {
		return importance
	}
	days := now.Sub(lastReferenced).Hours() / 24
	if days < 0 {
		days = 0
	}
	decay := 1.0 / (1.0 + days/30.0)
	boost := 1.0 + float64(referenceCount)*0.1
	return importance * decay * boost
}
