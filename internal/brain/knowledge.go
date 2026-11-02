package brain

import (
	"context"
	"encoding/json"

	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/persona"
)

type knowledgePromptItem struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Text   string `json:"text"`
}

type promptPlan struct {
	Messages  []provider.Message
	Knowledge *knowledgeBudgetAudit
}

// knowledgeBudgetAudit 只记录经过目标范围校验的候选ID和字节预算，不记录正文。
type knowledgeBudgetAudit struct {
	Enabled            bool     `json:"enabled"`
	Revision           string   `json:"revision"`
	BudgetPercent      int      `json:"budget_percent"`
	CandidateIDs       []string `json:"candidate_ids"`
	IncludedIDs        []string `json:"included_ids"`
	DroppedIDs         []string `json:"dropped_ids"`
	CapBytes           int      `json:"cap_bytes"`
	Bytes              int      `json:"bytes"`
	BasePromptEstimate int      `json:"base_prompt_estimate"`
	FixedEstimate      int      `json:"fixed_estimate"`
	MemoryEstimate     int      `json:"memory_estimate"`
	PromptEstimate     int      `json:"prompt_estimate"`
}

func emptySameShape[T any](items []T) []T {
	if items == nil {
		return nil
	}
	return items[:0]
}

// finishKnowledge 在原记忆预算完成后使用余量，不反向裁记忆或人格。
func finishKnowledge(ctx context.Context, k *knowledge.Context, p persona.Pack, section contextSection, eventJSON []byte, ch event.Channel, state string, base []provider.Message, limit, percent int) (promptPlan, error) {
	if percent < 0 || percent > 10 {
		return promptPlan{}, &Error{Kind: KindInput}
	}
	fixed := section
	fixed.Shared = emptySameShape(fixed.Shared)
	fixed.Recent = emptySameShape(fixed.Recent)
	fixed.CrossChannel = emptySameShape(fixed.CrossChannel)
	fixed.Records = emptySameShape(fixed.Records)
	withoutMemory, err := renderContext(ctx, p, fixed, eventJSON, ch, state)
	if err != nil {
		return promptPlan{}, err
	}
	baseEstimate, fixedEstimate := estimate(base), estimate(withoutMemory)
	memoryEstimate := baseEstimate - fixedEstimate
	if fixedEstimate > limit || memoryEstimate < 0 {
		return promptPlan{}, &Error{Kind: KindBudget}
	}
	capBytes := min(limit*percent/100, max(0, limit-fixedEstimate-memoryEstimate))
	a := &knowledgeBudgetAudit{Enabled: true, Revision: k.Revision, BudgetPercent: percent, CapBytes: capBytes, BasePromptEstimate: baseEstimate, FixedEstimate: fixedEstimate, MemoryEstimate: memoryEstimate, PromptEstimate: baseEstimate}
	var selected []knowledgePromptItem
	for _, item := range k.Items {
		if err := pendingError(ctx); err != nil {
			return promptPlan{}, contextFailure(err)
		}
		a.CandidateIDs = append(a.CandidateIDs, item.ID)
		projection := knowledgePromptItem{ID: item.ID, Source: item.Source, Text: item.Text}
		encoded, err := json.Marshal(projection)
		if err != nil {
			return promptPlan{}, &Error{Kind: KindInput, Cause: err}
		}
		trialBytes := a.Bytes + len(encoded)
		if len(selected) == 0 {
			trialBytes += len(`,"knowledge":[`) + len(`]`)
		} else {
			trialBytes++
		}
		if trialBytes <= capBytes {
			selected = append(selected, projection)
			a.Bytes = trialBytes
			a.IncludedIDs = append(a.IncludedIDs, item.ID)
		} else {
			a.DroppedIDs = append(a.DroppedIDs, item.ID)
		}
	}
	section.Knowledge = selected
	messages, err := renderContext(ctx, p, section, eventJSON, ch, state)
	if err != nil {
		return promptPlan{}, err
	}
	a.PromptEstimate = estimate(messages)
	if a.PromptEstimate > limit || a.PromptEstimate-baseEstimate != a.Bytes || a.Bytes > capBytes {
		return promptPlan{}, &Error{Kind: KindBudget}
	}
	return promptPlan{Messages: messages, Knowledge: a}, nil
}
