package reasoning

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateToolArgs(t *testing.T) {
	cases := []struct {
		name string
		args string
		ok   bool
	}{
		{ToolMemorySearch, `{"args":"白饭"}`, true},
		{ToolMemorySearch, ``, true},
		{ToolPresence, `{}`, true},
		{ToolPresence, ``, true},
		{ToolHistory, `{"args":"10"}`, true},
		{ToolMemorySearch, `not-json`, false},
		{ToolMemorySearch, `{"args":123}`, false},
		{ToolSendDanmaku, `{"args":"` + strings.Repeat("鱼", 41) + `"}`, false},
	}
	for _, tc := range cases {
		err := ValidateToolArgs(tc.name, tc.args)
		if tc.ok && err != nil {
			t.Fatalf("%s 应通过，实际：%v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s 应失败", tc.name)
		}
	}
}

func TestToolArgsText(t *testing.T) {
	got, err := ToolArgsText(ToolMemorySearch, `{"args":"昨天聊的梗"}`)
	if err != nil || got != "昨天聊的梗" {
		t.Fatalf("args 字段没有还原：%q %v", got, err)
	}
	got, err = ToolArgsText(ToolMemorySearch, `{"query":"白饭"}`)
	if err != nil || got != "白饭" {
		t.Fatalf("单字段对象没有还原：%q %v", got, err)
	}
	got, err = ToolArgsText(ToolPresence, `{}`)
	if err != nil || got != "" {
		t.Fatalf("无参数工具应为空：%q %v", got, err)
	}
}

func TestToolboxDefinitionsIncludeSchema(t *testing.T) {
	box, err := NewToolbox(
		Tool{Name: ToolMemorySearch, Desc: "查记忆", Run: func(_ context.Context, _ string) (string, error) { return "", nil }},
		Tool{Name: ToolPresence, Desc: "查状态", Run: func(_ context.Context, _ string) (string, error) { return "", nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defs := box.Definitions()
	if len(defs) != 2 || defs[0].Type != "function" || defs[0].Function.Name != ToolMemorySearch {
		t.Fatalf("定义不完整：%+v", defs)
	}
	var spec map[string]any
	if err := json.Unmarshal(defs[0].Function.Parameters, &spec); err != nil {
		t.Fatalf("参数 Schema 不是合法 JSON：%v", err)
	}
}
