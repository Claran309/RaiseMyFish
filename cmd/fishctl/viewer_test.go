package main

// 本文件测 `fishctl viewer`：参数解析、增量入库、检索输出与失败路径。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
)

// TestParseViewerArgs 子命令、默认值、边界与 help。
func TestParseViewerArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want viewerCommand
	}{
		{"embed", []string{"embed"}, viewerCommand{Action: "embed"}},
		{"search 默认", []string{"search", "--q", "白饭"}, viewerCommand{Action: "search", Query: "白饭", Limit: defaultViewerLimit}},
		{"search 去空白", []string{"search", "--q", "  爱要白饭  ", "--limit", "3"}, viewerCommand{Action: "search", Query: "爱要白饭", Limit: 3}},
		{"help", []string{"search", "--help"}, viewerCommand{Action: "help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseViewerArgs(tc.args, io.Discard)
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if got != tc.want {
				t.Fatalf("解析结果 = %+v，期望 %+v", got, tc.want)
			}
		})
	}
}

// TestParseViewerArgsRejectsBadInput 缺子命令、未知子命令、空查询与越界 limit 都拒绝。
func TestParseViewerArgsRejectsBadInput(t *testing.T) {
	cases := map[string][]string{
		"缺子命令":        nil,
		"未知子命令":       {"list"},
		"search 缺关键词": {"search"},
		"search 空白词":  {"search", "--q", "   "},
		"search 越界":   {"search", "--q", "鱼", "--limit", "0"},
		"search 超上限":  {"search", "--q", "鱼", "--limit", "51"},
		"search 多余参数": {"search", "--q", "鱼", "extra"},
		"embed 多余参数":  {"embed", "extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseViewerArgs(args, io.Discard); err == nil {
				t.Fatalf("参数 %v 未被拒绝", args)
			}
		})
	}
}

// viewerEmbedInput 造一条带三路源 hash 的档案输入（CAS 要求源 hash 非空）。
func viewerEmbedInput(uid, name string, texts, replies []string) viewer.EmbeddingInput {
	return viewer.EmbeddingInput{
		UID: uid, Name: name, Texts: texts, Replies: replies,
		SourceHashes: map[viewer.EmbeddingKind]string{
			viewer.KindIdentity:    "src-identity-" + uid,
			viewer.KindFacts:       "src-facts-" + uid,
			viewer.KindInteraction: "src-interaction-" + uid,
		},
	}
}

// mockViewerEmbedStore 是命令层测试用的增量向量化仓储。
type mockViewerEmbedStore struct {
	inputs  []viewer.EmbeddingInput
	indexes map[string]map[viewer.EmbeddingKind]viewer.EmbeddingIndex
	records []viewer.EmbeddingRecord
}

func (m *mockViewerEmbedStore) ViewerEmbeddingInputs(context.Context) ([]viewer.EmbeddingInput, error) {
	return m.inputs, nil
}

func (m *mockViewerEmbedStore) ExistingProfileEmbeddingIndexes(context.Context) (map[string]map[viewer.EmbeddingKind]viewer.EmbeddingIndex, error) {
	return m.indexes, nil
}

func (m *mockViewerEmbedStore) UpsertProfileEmbeddings(_ context.Context, records []viewer.EmbeddingRecord) (int, error) {
	m.records = append(m.records, records...)
	return len(records), nil
}

// TestExecuteViewerEmbedIncremental 增量入库输出摘要、进度与二次跳过。
func TestExecuteViewerEmbedIncremental(t *testing.T) {
	store := &mockViewerEmbedStore{inputs: []viewer.EmbeddingInput{
		viewerEmbedInput("u-1", "白饭哥", []string{"今天也要白饭"}, nil),
		viewerEmbedInput("u-2", "熟客", []string{"又来了"}, nil),
	}}
	embedder := &cliEmbedder{}
	var out, progress bytes.Buffer
	if err := executeViewerEmbed(context.Background(), store, embedder, testEmbedFingerprint, 1, &out, &progress); err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	var summary viewerEmbedOutput
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("输出不是合法JSON：%v %s", err, out.String())
	}
	// 每份档案拆 identity/facts/interaction 三路；两人都没有回复 → 2 条空 interaction 跳过。
	if summary.Total != 6 || summary.Pending != 4 || summary.Embedded != 4 || summary.Batches != 4 || summary.Skipped != 2 {
		t.Fatalf("入库摘要不正确：%+v", summary)
	}
	if embedder.calls != 4 || len(store.records) != 4 {
		t.Fatalf("批量写入不符：calls=%d records=%d", embedder.calls, len(store.records))
	}
	if !strings.Contains(progress.String(), "viewer embed: 已处理 1/4 条（1/4 批）") {
		t.Fatalf("进度日志不正确：%s", progress.String())
	}

	store.indexes = map[string]map[viewer.EmbeddingKind]viewer.EmbeddingIndex{}
	for _, record := range store.records {
		if store.indexes[record.UID] == nil {
			store.indexes[record.UID] = map[viewer.EmbeddingKind]viewer.EmbeddingIndex{}
		}
		store.indexes[record.UID][record.Kind] = viewer.EmbeddingIndex{
			TextHash:    record.Hash,
			SourceHash:  record.SourceHash,
			Fingerprint: record.Fingerprint,
		}
	}
	out.Reset()
	progress.Reset()
	if err := executeViewerEmbed(context.Background(), store, embedder, testEmbedFingerprint, 1, &out, &progress); err != nil {
		t.Fatalf("二次入库失败：%v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Skipped != 6 || summary.Pending != 0 || summary.Embedded != 0 {
		t.Fatalf("二次增量未全部跳过：%+v", summary)
	}
	if embedder.calls != 4 || progress.Len() != 0 {
		t.Fatalf("二次增量仍调用embedding：calls=%d progress=%q", embedder.calls, progress.String())
	}
}

// TestExecuteViewerEmbedReportsFailure embedding 失败时不出成功摘要。
func TestExecuteViewerEmbedReportsFailure(t *testing.T) {
	store := &mockViewerEmbedStore{inputs: []viewer.EmbeddingInput{viewerEmbedInput("u-1", "鱼", nil, nil)}}
	var out bytes.Buffer
	err := executeViewerEmbed(context.Background(), store, &cliEmbedder{err: errors.New("上游不可用")}, testEmbedFingerprint, 8, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "档案入库失败") {
		t.Fatalf("embedding失败未向上返回：%v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("失败时不应输出成功摘要：%s", out.String())
	}
}

// cliProfileSource 是检索命令测试用的档案来源。
type cliProfileSource struct {
	hits []store.ViewerProfileHit
}

func (s *cliProfileSource) KeywordProfiles(context.Context, viewer.EmbeddingKind, string, int) ([]store.ViewerProfileHit, error) {
	return s.hits, nil
}

func (s *cliProfileSource) SemanticProfiles(context.Context, viewer.EmbeddingKind, []float64, string, int) ([]store.ViewerProfileHit, error) {
	return nil, nil
}

// TestExecuteViewerSearchOutput 命中输出 uid/昵称/最近发言；空结果输出空数组。
func TestExecuteViewerSearchOutput(t *testing.T) {
	source := &cliProfileSource{hits: []store.ViewerProfileHit{{
		Profile: store.ViewerProfile{
			UID: "2002", Name: "白饭哥", Hits: 5, Replies: 2,
			LastSeen:    time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
			RecentTexts: []string{"今天也要白饭", "再来一碗", "谢谢鱼"},
		},
	}}}
	searcher, err := viewer.NewSearcher(source, nil, viewer.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	spec := viewerCommand{Action: "search", Query: "爱要白饭的人", Limit: 3}
	if err := executeViewerSearch(context.Background(), searcher, spec, &out); err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	var summary struct {
		Query    string       `json:"query"`
		Profiles []viewerItem `json:"profiles"`
		Count    int          `json:"count"`
	}
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("输出不是合法JSON：%v %s", err, out.String())
	}
	if summary.Query != "爱要白饭的人" || summary.Count != 1 || len(summary.Profiles) != 1 {
		t.Fatalf("检索摘要不符：%+v", summary)
	}
	item := summary.Profiles[0]
	if item.UID != "2002" || item.Name != "白饭哥" || len(item.RecentTexts) != 3 {
		t.Fatalf("命名字段不符：%+v", item)
	}

	empty, err := viewer.NewSearcher(&cliProfileSource{}, nil, viewer.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := executeViewerSearch(context.Background(), empty, spec, &out); err != nil {
		t.Fatalf("空结果检索失败：%v", err)
	}
	if !strings.Contains(out.String(), `"profiles":[]`) || !strings.Contains(out.String(), `"count":0`) {
		t.Errorf("空结果应输出空数组：%s", out.String())
	}
}

// TestRunViewerEmbedRequiresConfig embedding 未启用时不连库直接报错。
func TestRunViewerEmbedRequiresConfig(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte("[embedding]\nenabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runViewer(context.Background(), path, "", []string{"embed"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "embedding 未启用") {
		t.Fatalf("未在入库前检查embedding开关：%v", err)
	}
}
