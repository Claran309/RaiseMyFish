package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"RaiseMyFish/internal/knowledge"
)

const testCLIFingerprint = "test-cli-knowledge-fingerprint"

type cliEmbedder struct {
	calls  int
	inputs []string
	err    error
}

func (e *cliEmbedder) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	e.calls++
	e.inputs = append(e.inputs, inputs...)
	if e.err != nil {
		return nil, e.err
	}
	vectors := make([][]float64, len(inputs))
	for i := range inputs {
		vectors[i] = make([]float64, knowledge.EmbeddingDimension)
		vectors[i][i%knowledge.EmbeddingDimension] = 1
	}
	return vectors, nil
}

type cliEmbeddingStore struct {
	existing map[string]knowledge.EmbeddingMeta
	records  []knowledge.EmbeddingRecord
}

func newCLIEmbeddingStore() *cliEmbeddingStore {
	return &cliEmbeddingStore{existing: make(map[string]knowledge.EmbeddingMeta)}
}

func (s *cliEmbeddingStore) ExistingEmbeddings(context.Context) (map[string]knowledge.EmbeddingMeta, error) {
	result := make(map[string]knowledge.EmbeddingMeta, len(s.existing))
	for id, meta := range s.existing {
		result[id] = meta
	}
	return result, nil
}

func (s *cliEmbeddingStore) UpsertEmbeddings(_ context.Context, records []knowledge.EmbeddingRecord) (int, error) {
	for _, record := range records {
		s.records = append(s.records, record)
		s.existing[record.ID] = knowledge.EmbeddingMeta{TextHash: record.Hash, SourceHash: record.SourceHash, Fingerprint: record.Fingerprint}
	}
	return len(records), nil
}

func (s *cliEmbeddingStore) SemanticSearch(context.Context, []float64, string, int) ([]knowledge.SemanticHit, error) {
	return nil, nil
}

func writeCLIKnowledge(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	empty := "---\nschema: fish-knowledge/v1\nentries: []\n---\n"
	for _, name := range []string{"lore.md", "viewers.md", "games.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(empty), 0600); err != nil {
			t.Fatal(err)
		}
	}
	body := "---\nschema: fish-knowledge/v1\nentries:\n"
	for i := 0; i < 3; i++ {
		body += fmt.Sprintf("  - id: item-%d\n    title: 甲鱼%d\n    channel: [live]\n    keywords: [甲鱼]\n    tags: [测试]\n    priority: 50\n    visibility: public\n    source: test:item-%d\n    summary: 摘要%d\n    usage: |-\n      接法%d\n", i, i, i, i, i)
	}
	body += "---\n"
	if err := os.WriteFile(filepath.Join(dir, "memes.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestParseKnowledgeCommand(t *testing.T) {
	dir := writeCLIKnowledge(t)
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	path := filepath.Join(t.TempDir(), "fishd.toml")
	body := fmt.Sprintf("[knowledge]\nenabled = true\ndir = '%s'\n[embedding]\nenabled = false\n", filepath.ToSlash(dir))
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runKnowledge(context.Background(), path, "", []string{"embed"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "embedding 未启用") {
		t.Fatalf("未在入库前检查embedding开关：%v", err)
	}
	if err := runKnowledge(context.Background(), path, "", nil, io.Discard, io.Discard); !errors.Is(err, errKnowledgeUsage) {
		t.Fatalf("缺子命令未拒绝：%v", err)
	}
	if err := runKnowledge(context.Background(), path, "", []string{"embed", "extra"}, io.Discard, io.Discard); !errors.Is(err, errKnowledgeUsage) {
		t.Fatalf("多余参数未拒绝：%v", err)
	}
}

func TestExecuteKnowledgeEmbedIncrementalAndProgress(t *testing.T) {
	dir := writeCLIKnowledge(t)
	reader, err := knowledge.NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &cliEmbedder{}
	store := newCLIEmbeddingStore()
	var out, progress bytes.Buffer
	if err := executeKnowledgeEmbed(context.Background(), reader, embedder, store, 2, testCLIFingerprint, &out, &progress); err != nil {
		t.Fatal(err)
	}
	var summary knowledgeEmbedOutput
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("输出不是合法JSON：%v %s", err, out.String())
	}
	if summary.Total != 3 || summary.Pending != 3 || summary.Batches != 2 || summary.Embedded != 3 || summary.Skipped != 0 || len(summary.Revision) != 64 {
		t.Fatalf("入库摘要不正确：%+v", summary)
	}
	if len(store.records) != 3 || embedder.calls != 2 {
		t.Fatalf("批量写入不符：records=%d calls=%d", len(store.records), embedder.calls)
	}
	if !strings.Contains(progress.String(), "已处理 2/3 条（1/2 批）") || !strings.Contains(progress.String(), "已处理 3/3 条（2/2 批）") {
		t.Fatalf("进度日志不正确：%s", progress.String())
	}

	out.Reset()
	progress.Reset()
	if err := executeKnowledgeEmbed(context.Background(), reader, embedder, store, 2, testCLIFingerprint, &out, &progress); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Total != 3 || summary.Skipped != 3 || summary.Pending != 0 || summary.Embedded != 0 || summary.Batches != 0 {
		t.Fatalf("二次增量未全部跳过：%+v", summary)
	}
	if progress.Len() != 0 || embedder.calls != 2 || len(store.records) != 3 {
		t.Fatalf("二次增量仍调用embedding或写库：progress=%q calls=%d records=%d", progress.String(), embedder.calls, len(store.records))
	}
}

func TestExecuteKnowledgeEmbedReportsFailure(t *testing.T) {
	dir := writeCLIKnowledge(t)
	reader, err := knowledge.NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = executeKnowledgeEmbed(context.Background(), reader, &cliEmbedder{err: errors.New("上游不可用")}, newCLIEmbeddingStore(), 2, testCLIFingerprint, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "知识入库失败") {
		t.Fatalf("embedding失败未向上返回：%v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("失败时不应输出成功摘要：%s", out.String())
	}
}
