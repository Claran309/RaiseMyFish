package knowledge

// 本文件测「精确词表」（M09 的判定依据）：title/keywords/tags 都要覆盖，
// 混合召回必须原样转发底层 FileReader 的词表。

import (
	"context"
	"testing"
)

// TestExactTermsCoversTitleKeywordsTags 词表 = title + keywords + tags。
func TestExactTermsCoversTitleKeywordsTags(t *testing.T) {
	entry := testEntry("m1", "蚌埠住了")
	entry.Keywords = []string{"蚌埠", "绷不住"}
	tags := []string{"弹幕梗", "直播"}
	entry.Tags = &tags
	reader := loadEntries(t, entry)

	terms, err := reader.ExactTerms(context.Background())
	if err != nil {
		t.Fatalf("读取精确词表失败：%v", err)
	}
	want := map[string]bool{"蚌埠住了": true, "蚌埠": true, "绷不住": true, "弹幕梗": true, "直播": true}
	got := make(map[string]bool, len(terms))
	for _, term := range terms {
		got[term] = true
	}
	for term := range want {
		if !got[term] {
			t.Errorf("词表缺少 %q（实际 %v）", term, terms)
		}
	}
	// summary/usage 是解释性正文，不属于规范词。
	if got["原始接法"] {
		t.Error("usage 不应进入精确词表")
	}
}

// TestHybridReaderForwardsExactTerms 混合召回只加语义，不改精确词表。
func TestHybridReaderForwardsExactTerms(t *testing.T) {
	base := loadEntries(t, testEntry("m1", "蚌埠住了"))
	hybrid, err := NewHybridReader(base, &fakeEmbedder{}, newFakeEmbeddingStore(), HybridOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	terms, err := hybrid.ExactTerms(context.Background())
	if err != nil {
		t.Fatalf("混合召回未转发精确词表：%v", err)
	}
	found := false
	for _, term := range terms {
		if term == "蚌埠住了" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("词表未包含条目标题：%v", terms)
	}
}
