package main

import (
	"context"
	"testing"

	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/knowledgeeval"
)

// TestBM25RRFKeepsCandidatesAndOrder 只验证实验包装器不丢候选、不越界；
// 是否采用由 100 条 baseline 对比决定。
func TestBM25RRFKeepsCandidatesAndOrder(t *testing.T) {
	base := &fakeKnowledgeReader{items: []knowledge.Item{
		{ID: "a", Text: "术式公开 讲原理", Scenes: []string{"live"}},
		{ID: "b", Text: "术式公开 另一条", Scenes: []string{"live"}},
	}}
	reader := knowledgeeval.RRFReader{Base: base, MaxItems: 2}
	got, err := reader.Recall(context.Background(), knowledge.Query{
		Scope: knowledge.Scope{Channel: "live", Session: "room"}, Text: "术式公开",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Items[0].ID == "" {
		t.Fatalf("候选丢失：%+v", got.Items)
	}
}

type fakeKnowledgeReader struct{ items []knowledge.Item }

func (f *fakeKnowledgeReader) Recall(_ context.Context, query knowledge.Query) (knowledge.Context, error) {
	return knowledge.Context{Scope: query.Scope, Revision: "rev", Items: append([]knowledge.Item(nil), f.items...)}, nil
}
