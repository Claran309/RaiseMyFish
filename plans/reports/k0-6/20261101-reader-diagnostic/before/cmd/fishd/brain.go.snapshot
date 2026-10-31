package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// buildBrains 只装配可信配置和人格，不提供假的事件消费或Memory Reader。
func buildBrains(ctx context.Context, cfg config.Config, usage store.UsageRepo, logger *zap.Logger) (map[string]brain.Brain, persona.Pack, error) {
	if !cfg.LLM.Enabled {
		return nil, persona.Pack{}, nil
	}
	if err := cfg.LLM.Validate(); err != nil {
		return nil, persona.Pack{}, err
	}
	pack, err := persona.Load(ctx, cfg.LLM.PersonaDir)
	if err != nil {
		return nil, persona.Pack{}, fmt.Errorf("加载Brain人格：%w", err)
	}
	keys := make([]string, 0, len(cfg.LLM.Routing))
	for key := range cfg.LLM.Routing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	routes := make(map[string]brain.Brain, len(keys))
	cache := make(map[string]brain.Brain)
	for _, key := range keys {
		name, p, model, err := cfg.LLM.Resolve(key)
		if err != nil {
			return nil, persona.Pack{}, err
		}
		ref := name + ":" + model
		if ready, ok := cache[ref]; ok {
			routes[key] = ready
			continue
		}
		client, err := provider.NewOpenAI(name, p.BaseURL, p.APIKey, &http.Client{}, time.Duration(cfg.LLM.AttemptTimeoutMS)*time.Millisecond)
		if err != nil {
			return nil, persona.Pack{}, err
		}
		o := brain.DefaultOptions()
		o.Model = model
		o.MaxPromptTokens = cfg.LLM.MaxPromptTokens
		o.MaxReplyTokens = cfg.LLM.MaxReplyTokens
		o.AttemptTimeout = time.Duration(cfg.LLM.AttemptTimeoutMS) * time.Millisecond
		o.TotalTimeout = time.Duration(cfg.LLM.TotalTimeoutMS) * time.Millisecond
		o.Backoff = time.Duration(cfg.LLM.BackoffMS) * time.Millisecond
		o.MaxConcurrent = cfg.LLM.MaxConcurrent
		o.Temperature = cfg.LLM.Temperature
		if price, ok := p.Prices[model]; ok {
			o.InputUSDPerMillion = price.InputUSDPerMillion
			o.OutputUSDPerMillion = price.OutputUSDPerMillion
		}
		runtime, err := brain.New(client, usage, o, logger)
		if err != nil {
			return nil, persona.Pack{}, err
		}
		cache[ref] = runtime
		routes[key] = runtime
	}
	return routes, pack, nil
}

func printBrainDiagnostic(route string, routes map[string]brain.Brain, pack persona.Pack, out io.Writer) error {
	runtime, ok := routes[route]
	if !ok {
		return errors.New("Brain未启用或诊断路由不存在")
	}
	summary := struct {
		Route          string `json:"route"`
		Brain          string `json:"brain"`
		Persona        string `json:"persona"`
		PersonaVersion string `json:"persona_version"`
		ProviderCalled bool   `json:"provider_called"`
		MemoryReader   string `json:"memory_reader"`
	}{route, runtime.Name(), pack.Meta.Name, pack.Meta.Version, false, "not_implemented"}
	if err := json.NewEncoder(out).Encode(summary); err != nil {
		return fmt.Errorf("输出Brain诊断：%w", err)
	}
	return nil
}
