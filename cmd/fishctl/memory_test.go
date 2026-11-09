package main

// 0.5 补丁：fishctl memory 子命令测试，仓储用 mock 注入，不连 PG。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// mockMemoryRepo 记录收到的参数并按预设返回。
type mockMemoryRepo struct {
	list      []store.MemoryRecord
	listErr   error
	search    []store.MemoryRecord
	searchErr error
	counts    map[string]int
	countErr  error

	gotLimit int
	gotQuery string
}

func (m *mockMemoryRepo) ListRecords(_ context.Context, limit int) ([]store.MemoryRecord, error) {
	m.gotLimit = limit
	return m.list, m.listErr
}

func (m *mockMemoryRepo) SearchRecords(_ context.Context, q string, limit int) ([]store.MemoryRecord, error) {
	m.gotQuery, m.gotLimit = q, limit
	return m.search, m.searchErr
}

func (m *mockMemoryRepo) CountByKind(context.Context) (map[string]int, error) {
	return m.counts, m.countErr
}

// TestParseMemoryArgs 子命令、默认值、边界与 help。
func TestParseMemoryArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want memoryCommand
	}{
		{"list 默认", []string{"list"}, memoryCommand{Action: "list", Limit: defaultMemoryLimit}},
		{"list 指定条数", []string{"list", "--limit", "5"}, memoryCommand{Action: "list", Limit: 5}},
		{"search 默认", []string{"search", "--q", "猫"}, memoryCommand{Action: "search", Limit: defaultMemoryLimit, Query: "猫"}},
		{"search 去空白", []string{"search", "--q", "  带话  ", "--limit", "3"}, memoryCommand{Action: "search", Limit: 3, Query: "带话"}},
		{"stats", []string{"stats"}, memoryCommand{Action: "stats", Limit: defaultMemoryLimit}},
		{"embed", []string{"embed"}, memoryCommand{Action: "embed"}},
		{"freeze", []string{"freeze"}, memoryCommand{Action: "freeze"}},
		{"unfreeze", []string{"unfreeze"}, memoryCommand{Action: "unfreeze"}},
		{"help", []string{"list", "--help"}, memoryCommand{Action: "help"}},
		{"freeze help", []string{"freeze", "--help"}, memoryCommand{Action: "help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMemoryArgs(tc.args, io.Discard)
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if got != tc.want {
				t.Fatalf("解析结果 = %+v，期望 %+v", got, tc.want)
			}
		})
	}
}

// TestParseMemoryArgsRejectsBadInput 缺子命令、未知子命令、越界与多余参数都拒绝。
func TestParseMemoryArgsRejectsBadInput(t *testing.T) {
	cases := map[string][]string{
		"缺子命令":          nil,
		"未知子命令":         {"remove"},
		"list 越界":       {"list", "--limit", "0"},
		"list 超上限":      {"list", "--limit", "201"},
		"list 多余参数":     {"list", "extra"},
		"search 缺关键词":   {"search"},
		"search 空白词":    {"search", "--q", "   "},
		"stats 多余参数":    {"stats", "extra"},
		"embed 多余参数":    {"embed", "extra"},
		"freeze 多余参数":   {"freeze", "extra"},
		"unfreeze 多余参数": {"unfreeze", "extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMemoryArgs(args, io.Discard); err == nil {
				t.Fatalf("参数 %v 未被拒绝", args)
			}
		})
	}
}

// TestExecuteMemoryList 有数据/空数据的 JSON 输出与参数传递。
func TestExecuteMemoryList(t *testing.T) {
	created := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	repo := &mockMemoryRepo{list: []store.MemoryRecord{
		{
			Record:    memory.Record{ID: "mem-1", Importance: 0.9},
			Source:    "owner_direct",
			Kind:      "fact",
			Title:     "猫",
			CreatedAt: created,
		},
	}}

	var out bytes.Buffer
	if err := executeMemory(context.Background(), repo, memoryCommand{Action: "list", Limit: 10}, &out); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if repo.gotLimit != 10 {
		t.Errorf("limit = %d，期望 10", repo.gotLimit)
	}
	var got struct {
		Query   string       `json:"query"`
		Records []memoryItem `json:"records"`
		Count   int          `json:"count"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("JSON 解析失败：%v\n%s", err, out.String())
	}
	if got.Query != "" || got.Count != 1 || len(got.Records) != 1 {
		t.Fatalf("输出不符：%+v", got)
	}
	item := got.Records[0]
	if item.ID != "mem-1" || item.Kind != "fact" || item.Title != "猫" || item.Importance != 0.9 || !item.At.Equal(created) {
		t.Fatalf("记录字段不符：%+v", item)
	}

	empty := &mockMemoryRepo{}
	out.Reset()
	if err := executeMemory(context.Background(), empty, memoryCommand{Action: "list", Limit: 20}, &out); err != nil {
		t.Fatalf("空列表失败：%v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"records":[]`)) || !bytes.Contains(out.Bytes(), []byte(`"count":0`)) {
		t.Fatalf("空列表应输出 []：%s", out.String())
	}
}

// TestExecuteMemorySearch 关键词与 limit 透传，输出带 query。
func TestExecuteMemorySearch(t *testing.T) {
	repo := &mockMemoryRepo{search: []store.MemoryRecord{{
		Record: memory.Record{ID: "mem-2", ValidFrom: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		Kind:   "note",
		Title:  "带话",
	}}}

	var out bytes.Buffer
	if err := executeMemory(context.Background(), repo, memoryCommand{Action: "search", Limit: 7, Query: "带话"}, &out); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if repo.gotQuery != "带话" || repo.gotLimit != 7 {
		t.Fatalf("参数未透传：query=%q limit=%d", repo.gotQuery, repo.gotLimit)
	}
	var got struct {
		Query   string       `json:"query"`
		Records []memoryItem `json:"records"`
		Count   int          `json:"count"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Query != "带话" || got.Count != 1 || got.Records[0].ID != "mem-2" {
		t.Fatalf("输出不符：%+v", got)
	}
	// CreatedAt 缺失时回退 ValidFrom，避免输出零时间
	if !got.Records[0].At.Equal(repo.search[0].ValidFrom) {
		t.Errorf("At = %v，期望回退 ValidFrom %v", got.Records[0].At, repo.search[0].ValidFrom)
	}
}

// TestExecuteMemoryStats 按 kind 汇总并给出总数；空 map 输出 {}。
func TestExecuteMemoryStats(t *testing.T) {
	repo := &mockMemoryRepo{counts: map[string]int{"fact": 3, "promise": 2}}
	var out bytes.Buffer
	if err := executeMemory(context.Background(), repo, memoryCommand{Action: "stats"}, &out); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	var got struct {
		Counts map[string]int `json:"counts"`
		Total  int            `json:"total"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 5 || got.Counts["fact"] != 3 || got.Counts["promise"] != 2 {
		t.Fatalf("统计输出不符：%+v", got)
	}

	out.Reset()
	if err := executeMemory(context.Background(), &mockMemoryRepo{}, memoryCommand{Action: "stats"}, &out); err != nil {
		t.Fatalf("空统计失败：%v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"counts":{}`)) || !bytes.Contains(out.Bytes(), []byte(`"total":0`)) {
		t.Fatalf("空统计应输出 {}：%s", out.String())
	}
}

// TestExecuteMemoryErrors 查询错误与输出错误必须上抛。
func TestExecuteMemoryErrors(t *testing.T) {
	boom := errors.New("查询炸了")
	if err := executeMemory(context.Background(), &mockMemoryRepo{listErr: boom}, memoryCommand{Action: "list", Limit: 1}, io.Discard); !errors.Is(err, boom) {
		t.Errorf("list 错误未透传：%v", err)
	}
	if err := executeMemory(context.Background(), &mockMemoryRepo{searchErr: boom}, memoryCommand{Action: "search", Limit: 1, Query: "猫"}, io.Discard); !errors.Is(err, boom) {
		t.Errorf("search 错误未透传：%v", err)
	}
	if err := executeMemory(context.Background(), &mockMemoryRepo{countErr: boom}, memoryCommand{Action: "stats"}, io.Discard); !errors.Is(err, boom) {
		t.Errorf("stats 错误未透传：%v", err)
	}

	fail := failingWriter(func([]byte) (int, error) { return 0, errors.New("写不出去") })
	if err := executeMemory(context.Background(), &mockMemoryRepo{}, memoryCommand{Action: "list", Limit: 1}, fail); err == nil {
		t.Error("输出失败未报错")
	}
	if err := executeMemory(context.Background(), &mockMemoryRepo{}, memoryCommand{Action: "stats"}, fail); err == nil {
		t.Error("统计输出失败未报错")
	}
}

type failingWriter func([]byte) (int, error)

func (f failingWriter) Write(p []byte) (int, error) { return f(p) }

// TestRunMemoryRejectsUsageBeforeLoadingConfig 参数错误先于配置加载返回。
func TestRunMemoryRejectsUsageBeforeLoadingConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.toml")
	err := runMemory(context.Background(), missing, "", nil, io.Discard, io.Discard)
	if !errors.Is(err, errMemoryUsage) {
		t.Fatalf("期望用法错误，实际 %v", err)
	}
}

// TestRunMemoryWithoutLiveConfig 无 live 凭据时 memory list/stats 仍能通过配置加载进入数据库阶段。
func TestRunMemoryWithoutLiveConfig(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private-secret@127.0.0.1:1/fish?connect_timeout=1")
	dir := t.TempDir()
	path := filepath.Join(dir, "fishd.toml")
	body := "[db]\ntimeout_seconds=1\n[channels.live]\nenabled=true\n[collector.live]\nroom_id=\"\"\nself_id=\"\"\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"memory", "list"}, {"memory", "stats"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			err := run(context.Background(), append([]string{"--config", path, "--env-file", ""}, args...), io.Discard, io.Discard)
			if err == nil {
				t.Fatal("数据库不可达时应报错")
			}
			if strings.Contains(err.Error(), "直播采集") {
				t.Fatalf("memory 被 live 配置挡住：%v", err)
			}
			if !strings.Contains(err.Error(), "初始化PG") {
				t.Fatalf("未进入数据库连接阶段：%v", err)
			}
		})
	}
}

// TestRunRoutesMemoryCommand 顶层 run 能路由到 memory 并拒绝非法 limit。
func TestRunRoutesMemoryCommand(t *testing.T) {
	err := run(context.Background(), []string{"memory", "list", "--limit", "0"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("memory 路由未生效：%v", err)
	}
}

// TestExecuteMemoryFreezeTogglesStateFile 冻结/解冻只改状态文件，输出最终状态且重复调用幂等。
func TestExecuteMemoryFreezeTogglesStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "memory.frozen")
	old := memoryFreezeFile
	memoryFreezeFile = path
	t.Cleanup(func() { memoryFreezeFile = old })

	var out bytes.Buffer
	if err := executeMemoryFreeze(memoryCommand{Action: "freeze"}, &out); err != nil {
		t.Fatalf("冻结失败：%v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("冻结标记未创建：%v", err)
	}
	var got struct {
		Frozen bool   `json:"frozen"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("解析输出失败：%v\n%s", err, out.String())
	}
	if !got.Frozen || got.Path != path {
		t.Fatalf("冻结输出不符：%+v", got)
	}

	// 重复冻结保持幂等
	out.Reset()
	if err := executeMemoryFreeze(memoryCommand{Action: "freeze"}, &out); err != nil {
		t.Fatalf("重复冻结失败：%v", err)
	}

	// 解冻并确认文件删除、输出 frozen=false
	out.Reset()
	if err := executeMemoryFreeze(memoryCommand{Action: "unfreeze"}, &out); err != nil {
		t.Fatalf("解冻失败：%v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("解冻后标记仍存在：%v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("解析输出失败：%v\n%s", err, out.String())
	}
	if got.Frozen {
		t.Fatalf("解冻输出不符：%+v", got)
	}

	// 重复解冻保持幂等
	if err := executeMemoryFreeze(memoryCommand{Action: "unfreeze"}, io.Discard); err != nil {
		t.Fatalf("重复解冻失败：%v", err)
	}

	if err := executeMemoryFreeze(memoryCommand{Action: "list"}, io.Discard); !errors.Is(err, errMemoryUsage) {
		t.Errorf("非法 action 未拒绝：%v", err)
	}
	fail := failingWriter(func([]byte) (int, error) { return 0, errors.New("写不出去") })
	if err := executeMemoryFreeze(memoryCommand{Action: "freeze"}, fail); err == nil {
		t.Error("输出失败未报错")
	}
}

// TestRunMemoryFreezeWithoutConfigOrDatabase 冻结开关不依赖配置与数据库，缺配置也能执行。
func TestRunMemoryFreezeWithoutConfigOrDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.frozen")
	old := memoryFreezeFile
	memoryFreezeFile = path
	t.Cleanup(func() { memoryFreezeFile = old })

	missingConfig := filepath.Join(t.TempDir(), "missing.toml")
	var out bytes.Buffer
	if err := runMemory(context.Background(), missingConfig, "", []string{"freeze"}, &out, io.Discard); err != nil {
		t.Fatalf("缺配置时冻结失败：%v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("冻结标记未创建：%v", err)
	}

	out.Reset()
	if err := runMemory(context.Background(), missingConfig, "", []string{"unfreeze"}, &out, io.Discard); err != nil {
		t.Fatalf("缺配置时解冻失败：%v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("解冻后标记仍存在：%v", err)
	}
}

// mockMemoryEmbedStore 是命令层测试用的增量向量化仓储。
type mockMemoryEmbedStore struct {
	inputs  []memory.EmbeddingInput
	hashes  map[string]string
	records []memory.EmbeddingRecord
	// allowPrivate 记录命令层传给仓储的授权参数。
	allowPrivate bool
}

func (m *mockMemoryEmbedStore) MemoryEmbeddingInputs(_ context.Context, allowPrivate bool) ([]memory.EmbeddingInput, error) {
	m.allowPrivate = allowPrivate
	return m.inputs, nil
}

func (m *mockMemoryEmbedStore) ExistingMemoryEmbeddingHashes(context.Context) (map[string]string, error) {
	return m.hashes, nil
}

func (m *mockMemoryEmbedStore) UpsertMemoryEmbeddings(_ context.Context, records []memory.EmbeddingRecord) error {
	m.records = append(m.records, records...)
	return nil
}

// TestExecuteMemoryEmbedIncremental 增量入库输出摘要、进度与二次跳过。
func TestExecuteMemoryEmbedIncremental(t *testing.T) {
	store := &mockMemoryEmbedStore{inputs: []memory.EmbeddingInput{
		{ID: "mem-1", Title: "猫", Summary: "很乖"},
		{ID: "mem-2", Title: "", Summary: "今天晴天"},
	}}
	embedder := &cliEmbedder{}
	var out, progress bytes.Buffer
	if err := executeMemoryEmbed(context.Background(), store, embedder, 1, false, &out, &progress); err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	var summary memoryEmbedOutput
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("输出不是合法JSON：%v %s", err, out.String())
	}
	if summary.Total != 2 || summary.Pending != 2 || summary.Embedded != 2 || summary.Batches != 2 || summary.Skipped != 0 {
		t.Fatalf("入库摘要不正确：%+v", summary)
	}
	if summary.AllowPrivate || store.allowPrivate {
		t.Errorf("默认不应授权私密外发：summary=%+v store=%v", summary, store.allowPrivate)
	}
	if embedder.calls != 2 || len(store.records) != 2 {
		t.Fatalf("批量写入不符：calls=%d records=%d", embedder.calls, len(store.records))
	}
	if !strings.Contains(progress.String(), "memory embed: 已处理 1/2 条（1/2 批）") {
		t.Fatalf("进度日志不正确：%s", progress.String())
	}

	// 二次执行：hash 已写入，应全部跳过
	store.hashes = map[string]string{}
	for _, record := range store.records {
		store.hashes[record.ID] = record.Hash
	}
	out.Reset()
	progress.Reset()
	if err := executeMemoryEmbed(context.Background(), store, embedder, 1, false, &out, &progress); err != nil {
		t.Fatalf("二次入库失败：%v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Skipped != 2 || summary.Pending != 0 || summary.Embedded != 0 {
		t.Fatalf("二次增量未全部跳过：%+v", summary)
	}
	if embedder.calls != 2 || progress.Len() != 0 {
		t.Fatalf("二次增量仍调用embedding：calls=%d progress=%q", embedder.calls, progress.String())
	}
}

// TestExecuteMemoryEmbedPrivateGate 私密记忆默认不外发，授权后才发送；归档始终排除。
func TestExecuteMemoryEmbedPrivateGate(t *testing.T) {
	inputs := []memory.EmbeddingInput{
		{ID: "mem-public", Summary: "公开"},
		{ID: "mem-private", Summary: "私密", Private: true},
		{ID: "mem-archived", Summary: "归档", Archived: true},
	}

	store := &mockMemoryEmbedStore{inputs: inputs}
	embedder := &cliEmbedder{}
	var out bytes.Buffer
	if err := executeMemoryEmbed(context.Background(), store, embedder, 8, false, &out, io.Discard); err != nil {
		t.Fatalf("默认入库失败：%v", err)
	}
	var summary memoryEmbedOutput
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.AllowPrivate || summary.Embedded != 1 || summary.Skipped != 2 {
		t.Fatalf("默认授权摘要不符：%+v", summary)
	}
	if !slices.Equal(embedder.inputs, []string{"公开"}) {
		t.Fatalf("默认外发输入 = %v，期望只有公开内容", embedder.inputs)
	}

	// 显式授权：私密加入，归档仍然排除。
	store = &mockMemoryEmbedStore{inputs: inputs}
	embedder = &cliEmbedder{}
	out.Reset()
	if err := executeMemoryEmbed(context.Background(), store, embedder, 8, true, &out, io.Discard); err != nil {
		t.Fatalf("授权入库失败：%v", err)
	}
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.AllowPrivate || summary.Embedded != 2 || summary.Skipped != 1 {
		t.Fatalf("授权摘要不符：%+v", summary)
	}
	if !slices.Equal(embedder.inputs, []string{"公开", "私密"}) {
		t.Fatalf("授权外发输入 = %v，期望公开+私密", embedder.inputs)
	}
}

// TestExecuteMemoryEmbedReportsFailure embedding 失败时不出成功摘要。
func TestExecuteMemoryEmbedReportsFailure(t *testing.T) {
	store := &mockMemoryEmbedStore{inputs: []memory.EmbeddingInput{{ID: "mem-1", Summary: "鱼"}}}
	var out bytes.Buffer
	err := executeMemoryEmbed(context.Background(), store, &cliEmbedder{err: errors.New("上游不可用")}, 8, false, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "记忆入库失败") {
		t.Fatalf("embedding失败未向上返回：%v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("失败时不应输出成功摘要：%s", out.String())
	}
}

// TestRunMemoryEmbedRequiresConfig embedding 未启用时不连库直接报错。
func TestRunMemoryEmbedRequiresConfig(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte("[embedding]\nenabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runMemory(context.Background(), path, "", []string{"embed"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "embedding 未启用") {
		t.Fatalf("未在入库前检查embedding开关：%v", err)
	}
}
