package knowledgeeval

// B8 对照实验：同一批 100 条用例，比较「基座关键词」与「BM25+RRF」。
// 结论写进 docs/reports；测试只保证实验可重复，不断言必须提升。

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"RaiseMyFish/internal/knowledge"
)

func TestBM25RRFCompareBaseline(t *testing.T) {
	corpus := filepath.Join("..", "..", "personas", "dafeiyu", "knowledge")
	casesPath := filepath.Join("testdata", "cases.jsonl")
	data, err := os.ReadFile(casesPath)
	if err != nil {
		t.Fatal(err)
	}
	cases, err := ReadCases(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := knowledge.NewFileReader(context.Background(), corpus)
	if err != nil {
		t.Fatal(err)
	}
	// 权限用例使用合成 ACL 夹具；实验只改变公开检索路径。
	aclPath := filepath.Join("testdata", "batch-synthetic-acl.md")
	aclDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(aclDir, "memes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(aclPath); err == nil {
		if err := os.WriteFile(filepath.Join(aclDir, "memes", "batch-synthetic-acl.md"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// FileReader 要求五个固定段都存在；权限用例只关心 ACL 夹具。
	empty := []byte("---\nschema: fish-knowledge/v1\nentries: []\n---\n")
	for _, name := range []string{"lore.md", "memes.md", "viewers.md", "games.md"} {
		if err := os.WriteFile(filepath.Join(aclDir, name), empty, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	synthetic, err := knowledge.NewFileReader(context.Background(), aclDir)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := (Evaluator{Public: reader, Synthetic: synthetic, Budget: false}).Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	// 实验组只对公开检索生效；权限用例仍用同一 Reader（本实验不评估权限增强）。
	experiment, err := (Evaluator{Public: RRFReader{Base: reader}, Synthetic: synthetic, Budget: false}).Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	baseScore := float64(baseline.Metrics.PositiveHits) / 60 * 100
	expScore := float64(experiment.Metrics.PositiveHits) / 60 * 100
	t.Logf("baseline positive=%d/60 (%.1f) relevant=%d/%d neg_false=%d",
		baseline.Metrics.PositiveHits, baseScore, baseline.Metrics.Relevant, baseline.Metrics.Returned, baseline.Metrics.NegativeFalse)
	t.Logf("bm25+rrf positive=%d/60 (%.1f) relevant=%d/%d neg_false=%d delta=%+.1f",
		experiment.Metrics.PositiveHits, expScore, experiment.Metrics.Relevant, experiment.Metrics.Returned,
		experiment.Metrics.NegativeFalse, expScore-baseScore)
	// 安全底线：实验不能增加负例误返或越权返回。
	if experiment.Metrics.NegativeFalse > baseline.Metrics.NegativeFalse {
		t.Fatalf("实验增加负例误返：%d > %d", experiment.Metrics.NegativeFalse, baseline.Metrics.NegativeFalse)
	}
	if experiment.Metrics.ForbiddenReturns > baseline.Metrics.ForbiddenReturns {
		t.Fatalf("实验增加越权返回")
	}
}
