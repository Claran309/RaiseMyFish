package knowledgeeval

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// BudgetAudit 的单位是保守UTF-8字节估计，不是实际模型token或真实费用。
type BudgetAudit struct {
	Revision       string   `json:"revision"`
	CandidateIDs   []string `json:"candidate_ids"`
	IncludedIDs    []string `json:"included_ids"`
	DroppedIDs     []string `json:"dropped_ids"`
	Percent        int      `json:"budget_percent"`
	Cap            int      `json:"cap_bytes"`
	Bytes          int      `json:"bytes"`
	BaseEstimate   int      `json:"base_prompt_estimate"`
	PromptEstimate int      `json:"prompt_estimate"`
	ProviderCalls  int      `json:"fake_provider_calls"`
	UsageRows      int      `json:"fake_usage_rows"`
	Passed         bool     `json:"passed"`
}

// offlineProvider 只在内存累计大小和ID，不保存请求正文，更不创建网络客户端。
type offlineProvider struct {
	calls    int
	estimate int
	system   string
	ids      []string
}

func (p *offlineProvider) Name() string { return "offline-fixed" }
func (p *offlineProvider) Chat(ctx context.Context, r provider.Request) (provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return provider.Response{}, err
	}
	p.calls++
	p.estimate = 64
	p.ids = nil
	for _, m := range r.Messages {
		p.estimate += len(m.Content)
		if m.Role == "system" {
			p.system = m.Content
		}
		if m.Role == "user" {
			start, end := -1, -1
			for i := 0; i+len("<context>") <= len(m.Content); i++ {
				if m.Content[i:i+len("<context>")] == "<context>" {
					start = i + len("<context>")
					break
				}
			}
			if start >= 0 {
				for i := start; i+len("</context>") <= len(m.Content); i++ {
					if m.Content[i:i+len("</context>")] == "</context>" {
						end = i
						break
					}
				}
			}
			if start < 0 || end < start {
				return provider.Response{}, errors.New("离线请求缺context段")
			}
			var section struct {
				Knowledge []struct {
					ID string `json:"id"`
				} `json:"knowledge"`
			}
			if err := json.Unmarshal([]byte(m.Content[start:end]), &section); err != nil {
				return provider.Response{}, err
			}
			for _, item := range section.Knowledge {
				p.ids = append(p.ids, item.ID)
			}
		}
	}
	return provider.Response{Text: "离线固定回复", Usage: provider.Usage{}}, nil
}

type offlineUsage struct{ rows int }

func (u *offlineUsage) RecordUsage(ctx context.Context, row store.Usage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := row.Validate(); err != nil {
		return err
	}
	if row.InputTokens != nil || row.OutputTokens != nil || row.CostUSD != nil {
		return errors.New("离线用量必须未知")
	}
	u.rows++
	return nil
}

// CheckBudget 使用真实Brain两次组装：候选知识与同形空知识；fake仅替代Provider/Usage。
func CheckBudget(ctx context.Context, q knowledge.Query, k knowledge.Context) (BudgetAudit, error) {
	p, u := &offlineProvider{}, &offlineUsage{}
	o := brain.DefaultOptions()
	o.Model = "offline-fixed"
	o.MaxPromptTokens = 32768
	o.KnowledgeBudgetPercent = 10
	b, err := brain.New(p, u, o, zap.NewNop())
	if err != nil {
		return BudgetAudit{}, err
	}
	scope := memory.Scope{Channel: q.Scope.Channel, Session: q.Scope.Session, Private: q.Scope.Private}
	pack := persona.Pack{Meta: persona.Meta{Name: "offline-budget", Version: "v1"}, Soul: "离线预算测试身份", Rules: "仅验证资料预算。", Samples: "固定样本。", Contexts: map[string]string{"live": "公开直播", "comment": "公开评论", "dm": "私聊", "qq_private": "QQ私聊", "qq_group": "QQ群"}}
	task := brain.Task{ID: "offline-budget", EventID: "offline-event", Channel: q.Scope.Channel, Session: q.Scope.Session}
	in := brain.ContextInput{Event: event.Event{ID: task.EventID, Channel: q.Scope.Channel, Dir: event.DirIn, Session: q.Scope.Session, Text: q.Text, Private: q.Scope.Private, At: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}, Persona: pack, Memory: memory.Context{Scope: scope}, Shared: memory.SharedState{Scope: scope}, Knowledge: &k}
	reply, err := b.Reply(ctx, task, in)
	if err != nil {
		return BudgetAudit{}, err
	}
	actualEstimate, actualIDs, actualSystem := p.estimate, slices.Clone(p.ids), p.system
	var meta struct {
		Knowledge *BudgetAudit `json:"knowledge"`
	}
	if err := json.Unmarshal(reply.Meta, &meta); err != nil {
		return BudgetAudit{}, err
	}
	if meta.Knowledge == nil {
		return BudgetAudit{}, errors.New("Brain缺知识预算审计")
	}
	audit := *meta.Knowledge
	empty := k
	empty.Items = nil
	in.Knowledge = &empty
	if _, err := b.Reply(ctx, task, in); err != nil {
		return BudgetAudit{}, err
	}
	audit.ProviderCalls = p.calls
	audit.UsageRows = u.rows
	audit.Passed = audit.Percent == 10 && audit.Bytes >= 0 && audit.Bytes == actualEstimate-p.estimate && audit.Bytes <= audit.Cap && audit.Cap <= 32768*10/100 && audit.PromptEstimate == actualEstimate && audit.BaseEstimate == p.estimate && actualEstimate <= 32768 && actualSystem == pack.SystemPrompt() && p.system == pack.SystemPrompt() && slices.Equal(actualIDs, audit.IncludedIDs) && audit.ProviderCalls == 2 && audit.UsageRows == 2
	if !audit.Passed {
		return audit, errors.New("知识实际序列化预算或静态人格不符合约束")
	}
	return audit, nil
}
