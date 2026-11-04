package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/knowledge"
)

type knowledgeDiagnostic struct {
	Enabled           bool   `json:"enabled"`
	Assembled         bool   `json:"assembled"`
	Revision          string `json:"revision,omitempty"`
	Entries           int    `json:"entries"`
	BudgetPercent     int    `json:"budget_percent"`
	PipelineConnected bool   `json:"pipeline_connected"`
}

type knowledgeAssembly struct {
	Reader knowledge.Reader
	// Base 始终指向文件快照；热重载与定时增量只操作它，语义包装不改变快照生命周期。
	Base       *knowledge.FileReader
	Embedder   knowledge.Embedder
	Store      knowledge.EmbeddingStore
	Diagnostic knowledgeDiagnostic
}

type knowledgeSetupError struct{ cause error }

func (e *knowledgeSetupError) Error() string { return "装配知识Reader失败" }
func (e *knowledgeSetupError) Unwrap() error { return e.cause }

// buildKnowledge 关闭时不触碰目录，启用失败不降级为空命中。
func buildKnowledge(ctx context.Context, cfg config.Knowledge) (knowledgeAssembly, error) {
	result := knowledgeAssembly{Diagnostic: knowledgeDiagnostic{Enabled: cfg.Enabled, BudgetPercent: cfg.BudgetPercent}}
	if err := cfg.Validate(); err != nil {
		return result, err
	}
	if ctx == nil {
		return result, &knowledgeSetupError{cause: errors.New("装配ctx不能为空")}
	}
	if err := ctx.Err(); err != nil {
		return result, &knowledgeSetupError{cause: err}
	}
	if !cfg.Enabled {
		return result, nil
	}
	reader, err := knowledge.NewFileReader(ctx, cfg.Dir)
	if err != nil {
		return result, &knowledgeSetupError{cause: fmt.Errorf("加载知识快照：%w", err)}
	}
	info, err := reader.SnapshotInfo()
	if err != nil {
		return result, &knowledgeSetupError{cause: err}
	}
	result.Reader = reader
	result.Base = reader
	result.Diagnostic.Assembled = true
	result.Diagnostic.Revision = info.Revision
	result.Diagnostic.Entries = info.Entries
	return result, nil
}

// printCheckDiagnostic 只展示已装配状态，不调用Brain/Reader查询，不写LLM账本。
func printCheckDiagnostic(cfg config.Config, routes map[string]brain.Brain, k knowledgeAssembly, out io.Writer) error {
	result := struct {
		Database                  string              `json:"database"`
		LLMEnabled                bool                `json:"llm_enabled"`
		BrainRoutes               int                 `json:"brain_routes"`
		ProviderCalled            bool                `json:"provider_called"`
		UsageWritten              bool                `json:"usage_written"`
		Knowledge                 knowledgeDiagnostic `json:"knowledge"`
		LiveCollector             liveDiagnostic      `json:"live_collector"`
		BusinessPipelineConnected bool                `json:"business_pipeline_connected"`
	}{Database: "ready", LLMEnabled: cfg.LLM.Enabled, BrainRoutes: len(routes), Knowledge: k.Diagnostic, LiveCollector: liveDiagnostic{Enabled: cfg.Channels.Live.Enabled, Configured: cfg.Channels.Live.Enabled}}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return fmt.Errorf("输出装配诊断：%w", err)
	}
	return nil
}
