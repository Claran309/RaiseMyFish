package main

// M6：记忆巩固默认 dry-run；--apply 才写库，且只执行 archive/supersede。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/memory"
)

type consolidationSource interface {
	Candidates(context.Context, int) ([]memory.ConsolidationCandidate, error)
	Apply(context.Context, []memory.ConsolidationAction) (int, error)
}

var _ consolidationSource = (*database.ConsolidationRepo)(nil)

// executeMemoryConsolidate 产出计划；--apply 时执行并回显实际影响行数。
func executeMemoryConsolidate(ctx context.Context, repo consolidationSource, apply bool, out io.Writer) error {
	candidates, err := repo.Candidates(ctx, 2000)
	if err != nil {
		return err
	}
	plan := memory.BuildConsolidationPlan(candidates, memory.ConsolidateOptions{})
	applied := 0
	if apply && len(plan.Actions) > 0 {
		applied, err = repo.Apply(ctx, plan.Actions)
		if err != nil {
			return err
		}
		plan.DryRun = false
	}
	payload := struct {
		memory.ConsolidationPlan
		Applied int `json:"applied"`
	}{ConsolidationPlan: plan, Applied: applied}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}
