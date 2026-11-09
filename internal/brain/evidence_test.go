package brain

// 本文件测 B2/N03：ReAct 回落的工具证据作为资料段进入单步 prompt。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"RaiseMyFish/internal/knowledge"
)

// TestPrepareInjectsToolEvidence 工具证据以 tool_evidence 资料段注入，
// 不带思维文本，也不改变 system 段。
func TestPrepareInjectsToolEvidence(t *testing.T) {
	task, in := fixture()
	in.Evidence = []Evidence{{Tool: "history", Args: "5", Result: "刚才在聊吃鱼"}}
	p, u := &fakeProvider{}, &fakeUsage{}
	if _, err := makeBrain(t, p, u, options()).Reply(context.Background(), task, in); err != nil {
		t.Fatal(err)
	}
	user := p.last().Messages[1].Content
	if p.last().Messages[0].Content != in.Persona.SystemPrompt() {
		t.Fatal("工具证据改变了 system 段")
	}
	var data struct {
		ToolEvidence []struct {
			Tool   string `json:"tool"`
			Args   string `json:"args"`
			Result string `json:"result"`
		} `json:"tool_evidence"`
	}
	if err := json.Unmarshal([]byte(sectionBody(t, user, "context")), &data); err != nil {
		t.Fatalf("context 不是合法 JSON：%v", err)
	}
	if len(data.ToolEvidence) != 1 || data.ToolEvidence[0].Tool != "history" || data.ToolEvidence[0].Args != "5" || data.ToolEvidence[0].Result != "刚才在聊吃鱼" {
		t.Fatalf("工具证据投影不符：%+v", data.ToolEvidence)
	}
	if strings.Contains(user, "thought") {
		t.Fatal("证据不应包含思维字段")
	}
	if !strings.Contains(user, "tool_evidence 是本轮已查证的工具结果") {
		t.Fatalf("有证据时 instructions 未提示可直接使用：%s", user)
	}

	// 无证据时保持原有 instructions 与 JSON 形状，不额外增加段落。
	task2, in2 := fixture()
	p2 := &fakeProvider{}
	if _, err := makeBrain(t, p2, &fakeUsage{}, options()).Reply(context.Background(), task2, in2); err != nil {
		t.Fatal(err)
	}
	plain := p2.last().Messages[1].Content
	if strings.Contains(sectionBody(t, plain, "context"), "tool_evidence") {
		t.Fatal("无证据时不应出现 tool_evidence 字段")
	}
	if strings.Contains(plain, "tool_evidence 是本轮已查证的工具结果") {
		t.Fatal("无证据时不应追加提示")
	}
}

// TestPrepareKeepsEvidenceWithKnowledge 知识注入路径（finishKnowledge）同样保留证据段。
func TestPrepareKeepsEvidenceWithKnowledge(t *testing.T) {
	task, in := fixture()
	in.Evidence = []Evidence{{Tool: "knowledge_search", Args: "梗", Result: "知识：拉拉"}}
	in = withKnowledge(in, knowledgeItem("k1", "拉拉是梗"))
	prepared, err := Prepare(context.Background(), task, in, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	body := sectionBody(t, prepared.Context, "context")
	var data struct {
		Knowledge    []struct{ ID string }   `json:"knowledge"`
		ToolEvidence []struct{ Tool string } `json:"tool_evidence"`
	}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Knowledge) != 1 || data.Knowledge[0].ID != "k1" || len(data.ToolEvidence) != 1 || data.ToolEvidence[0].Tool != "knowledge_search" {
		t.Fatalf("知识与证据未同时保留：%s", body)
	}
}

// TestPrepareRejectsInvalidEvidence 证据来自内部 ReAct，但 Prepare 仍按统一口径校验。
func TestPrepareRejectsInvalidEvidence(t *testing.T) {
	tooMany := make([]Evidence, maxEvidenceItems+1)
	for i := range tooMany {
		tooMany[i] = Evidence{Tool: "history", Result: "x"}
	}
	cases := []struct {
		name string
		list []Evidence
	}{
		{"NUL", []Evidence{{Tool: "history", Result: "坏\x00片段"}}},
		{"空工具名", []Evidence{{Tool: "  ", Result: "x"}}},
		{"工具名过长", []Evidence{{Tool: strings.Repeat("a", maxEvidenceToolRunes+1), Result: "x"}}},
		{"参数过长", []Evidence{{Tool: "history", Args: strings.Repeat("a", maxEvidenceArgsRunes+1), Result: "x"}}},
		{"结果过长", []Evidence{{Tool: "history", Result: strings.Repeat("a", maxEvidenceResultRunes+1)}}},
		{"条数过多", tooMany},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task, in := fixture()
			in.Evidence = tc.list
			_, err := Prepare(context.Background(), task, in, DefaultBudget())
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != KindInput {
				t.Fatalf("应拒绝非法证据，实际 %v", err)
			}
		})
	}
}

// TestPreparedKnowledgeInjected 只有真正写进资料段的知识才算命中。
func TestPreparedKnowledgeInjected(t *testing.T) {
	task, in := fixture()
	prepared, err := Prepare(context.Background(), task, in, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if prepared.KnowledgeInjected() {
		t.Fatal("没有知识上下文时不应视为已注入")
	}

	in = withKnowledge(in) // 零命中：范围与版本存在，但没有条目
	prepared, err = Prepare(context.Background(), task, in, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if prepared.KnowledgeInjected() {
		t.Fatal("零命中不应视为已注入（工具要保留）")
	}

	in = withKnowledge(in, knowledgeItem("k1", "拉拉是梗"))
	prepared, err = Prepare(context.Background(), task, in, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.KnowledgeInjected() {
		t.Fatal("命中条目应视为已注入")
	}
}

// TestKnowledgeScopeMismatchStillRejected 防回归：证据加入后知识范围校验仍然生效。
func TestKnowledgeScopeMismatchStillRejected(t *testing.T) {
	task, in := fixture()
	in.Knowledge = &knowledge.Context{Scope: knowledge.Scope{Channel: "live", Session: "other"}}
	_, err := Prepare(context.Background(), task, in, DefaultBudget())
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindInput {
		t.Fatalf("知识范围不一致应被拒绝，实际 %v", err)
	}
}
