package mcp

import (
	"testing"

	"RaiseMyFish/internal/reasoning"
)

// TestCatalogCoversReasoningTools 保证 reasoning 的工具名都有 MCP schema，避免新增工具被静默跳过。
func TestCatalogCoversReasoningTools(t *testing.T) {
	names := []string{
		reasoning.ToolMemorySearch,
		reasoning.ToolKnowledgeSearch,
		reasoning.ToolPresence,
		reasoning.ToolHistory,
		reasoning.ToolViewerMemory,
		reasoning.ToolViewerSearch,
		reasoning.ToolReplyHistory,
		reasoning.ToolLiveStatus,
		reasoning.ToolQuota,
		reasoning.ToolNow,
		reasoning.ToolVision,
		reasoning.ToolWebSearch,
		reasoning.ToolGift,
		reasoning.ToolFollow,
		reasoning.ToolLike,
		reasoning.ToolDM,
		reasoning.ToolPostDynamic,
	}
	if len(toolSpecs) != len(names) {
		t.Fatalf("schema 数量 %d 与 reasoning 工具数量 %d 不一致", len(toolSpecs), len(names))
	}
	for _, name := range names {
		spec, ok := toolSpecs[name]
		if !ok {
			t.Errorf("工具 %s 缺少 MCP schema", name)
			continue
		}
		if spec.Description == "" || spec.Schema.Type != "object" {
			t.Errorf("工具 %s 的 schema 元数据不完整：%+v", name, spec)
		}
	}
}
