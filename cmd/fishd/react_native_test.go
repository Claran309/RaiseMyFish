package main

import (
	"context"
	"testing"

	"RaiseMyFish/internal/brain/provider"
)

type fakeToolProvider struct {
	text        string
	unsupported bool
	toolCalled  bool
}

func (f *fakeToolProvider) Name() string { return "fake" }

func (f *fakeToolProvider) Chat(_ context.Context, _ provider.Request) (provider.Response, error) {
	return provider.Response{Text: f.text}, nil
}

func (f *fakeToolProvider) ChatWithTools(_ context.Context, in provider.ToolRequest) (provider.ToolResponse, error) {
	f.toolCalled = true
	if f.unsupported {
		return provider.ToolResponse{}, &provider.Error{Kind: provider.KindUnsupported, StatusCode: 400}
	}
	return provider.ToolResponse{Text: f.text}, nil
}

func TestProviderLLMCompleteWithTools(t *testing.T) {
	prov := &fakeToolProvider{text: "原生结果"}
	llm := &providerLLM{blueprint: &reasoningModel{client: prov, model: "m", maxTokens: 8}}
	got, err := llm.CompleteWithTools(context.Background(), []provider.Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}, nil)
	if err != nil || got.Text != "原生结果" || !prov.toolCalled {
		t.Fatalf("原生调用失败：%+v err=%v called=%v", got, err, prov.toolCalled)
	}
}

func TestProviderLLMCompleteWithToolsFallsBackWhenUnsupported(t *testing.T) {
	prov := &fakeToolProvider{text: "文本兜底", unsupported: true}
	llm := &providerLLM{blueprint: &reasoningModel{client: prov, model: "m", maxTokens: 8}}
	got, err := llm.CompleteWithTools(context.Background(), []provider.Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}, []provider.ToolDefinition{{Type: "function", Function: provider.ToolFunction{Name: "x"}}})
	if err != nil || got.Text != "文本兜底" {
		t.Fatalf("不支持 tools 时没有回落文本：%+v err=%v", got, err)
	}
}

func TestProviderLLMCompleteWithToolsFallbackKeepsToolResults(t *testing.T) {
	prov := &fakeToolProvider{text: "带工具观察", unsupported: true}
	llm := &providerLLM{blueprint: &reasoningModel{client: prov, model: "m", maxTokens: 8}}
	got, err := llm.CompleteWithTools(context.Background(), []provider.Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "u"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Function: provider.ToolCallFunction{Name: "x"}}}},
		{Role: "tool", Content: "工具结果内容", ToolCallID: "c1"},
	}, nil)
	if err != nil || got.Text != "带工具观察" {
		t.Fatalf("回落失败：%+v %v", got, err)
	}
}
