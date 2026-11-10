package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func viewerTestEntry() ViewerEntry {
	return ViewerEntry{
		UID:       "42",
		Name:      "白饭哥",
		Summary:   "性格：话痨；偏好：猫猫；口头禅：绷不住了；关系：常被鱼回；最近话题：白饭",
		Stats:     "说过 12 次 / 鱼回过 3 次 / 最后出现 2026-11-10",
		Keywords:  []string{"绷不住了", "猫猫"},
		UpdatedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
	}
}

func findViewerEntry(t *testing.T, reader *FileReader, id string) entry {
	t.Helper()
	for _, e := range reader.current().entries {
		if e.item.ID == id {
			return e
		}
	}
	t.Fatalf("知识库缺少条目 %s", id)
	return entry{}
}

func TestViewerWriterUpsertAndReload(t *testing.T) {
	dir := knowledgeDir(t)
	writer, err := NewViewerWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	input := viewerTestEntry()
	if err := writer.Upsert(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	id := viewerKnowledgeID(input.UID)
	reader := embeddingReader(t, dir)
	e := findViewerEntry(t, reader, id)
	if e.item.Private {
		t.Fatal("自动侧写必须是公开条目")
	}
	if len(e.item.Scenes) != 5 {
		t.Fatalf("侧写应对五个场景可见：%+v", e.item.Scenes)
	}
	if !strings.Contains(e.title, "白饭哥") || !strings.Contains(e.usage, "性格：话痨") {
		t.Fatalf("侧写正文未写入 usage：title=%q usage=%q", e.title, e.usage)
	}
	for _, want := range []string{"偏好：猫猫", "口头禅：绷不住了", "关系：常被鱼回", "最近话题：白饭"} {
		if !strings.Contains(e.usage, want) {
			t.Fatalf("usage 缺少 %q：%q", want, e.usage)
		}
	}
	if !strings.Contains(e.summary, "统计：说过 12 次") || !strings.Contains(e.summary, "2026-11-10") {
		t.Fatalf("统计或侧写日期未写入 summary：%q", e.summary)
	}
	if strings.Contains(e.summary, input.UID) || strings.Contains(e.usage, input.UID) {
		t.Fatal("公开知识条目不应包含 UID 明文")
	}
	text := embeddingText(e)
	if !strings.Contains(text, "性格：话痨") {
		t.Fatalf("侧写正文必须进入 embedding 输入：%q", text)
	}
	if strings.Contains(text, "说过 12 次") {
		t.Fatalf("统计不应进入 embedding 输入：%q", text)
	}
	joined := strings.Join(e.keywords, ",")
	if !strings.Contains(joined, "白饭哥") || !strings.Contains(joined, "绷不住了") {
		t.Fatalf("昵称或口头禅未进入关键词：%+v", e.keywords)
	}

	// 同一 UID 再次侧写：覆盖旧条目而不是堆新条目。
	input.Summary = "性格：安静；偏好：钓鱼"
	if err := writer.Upsert(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	reader = embeddingReader(t, dir)
	count := 0
	for _, item := range reader.current().entries {
		if item.item.ID == id {
			count++
			if !strings.Contains(item.usage, "性格：安静") || strings.Contains(item.usage, "话痨") {
				t.Fatalf("第二次侧写未覆盖：%q", item.usage)
			}
		}
	}
	if count != 1 {
		t.Fatalf("同一观众应只有一条自动条目：%d", count)
	}
}

func TestViewerWriterKeepsManualEntries(t *testing.T) {
	dir := knowledgeDir(t)
	manual := testEntry("viewer-manual-1", "手工观众")
	manual.Source = "manual:test"
	manual.Summary = "人工维护的观众资料"
	manual.Usage = "人工接法"
	writeEmbeddingEntries(t, dir, "viewers.md", manual)

	writer, err := NewViewerWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Upsert(context.Background(), viewerTestEntry()); err != nil {
		t.Fatal(err)
	}
	reader := embeddingReader(t, dir)
	got := findViewerEntry(t, reader, "viewer-manual-1")
	if got.summary != "人工维护的观众资料" {
		t.Fatalf("人工条目被改写：%+v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, viewerKnowledgeFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "viewer-manual") && !strings.Contains(string(data), "viewer-manual-1") {
		t.Fatalf("人工条目未保留在磁盘：%s", data)
	}
	if !strings.Contains(string(data), "禁止把私信、私密画像或合成测试人物写入此公开知识文件") {
		t.Fatalf("自动维护说明丢失：%s", data)
	}
}

func TestViewerWriterRejectsManualIDSquatting(t *testing.T) {
	dir := knowledgeDir(t)
	input := viewerTestEntry()
	id := viewerKnowledgeID(input.UID)
	squatted := testEntry(id, "被占用的手工条目")
	squatted.Source = "manual:test"
	squatted.Summary = "人工资料"
	writeEmbeddingEntries(t, dir, "viewers.md", squatted)

	writer, err := NewViewerWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Upsert(context.Background(), input); err == nil {
		t.Fatal("同 ID 的人工条目不应被自动侧写覆盖")
	}
}

func TestViewerWriterValidation(t *testing.T) {
	dir := knowledgeDir(t)
	writer, err := NewViewerWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Upsert(nil, viewerTestEntry()); err == nil {
		t.Fatal("nil ctx 应拒绝")
	}
	bad := []ViewerEntry{
		{Name: "甲", Summary: "性格：安静"},
		{UID: "1", Summary: "性格：安静"},
		{UID: "1", Name: "甲"},
		{UID: "1", Name: " \n\t ", Summary: "性格：安静"},
	}
	for _, input := range bad {
		if err := writer.Upsert(context.Background(), input); err == nil {
			t.Fatalf("非法条目未拒绝：%+v", input)
		}
	}
	if _, err := NewViewerWriter(""); err == nil {
		t.Fatal("空目录应拒绝")
	}
}

// TestViewerWriterCleansControlCharacters 写入前完整解析校验，控制字符必须被清洗而不是写坏文件。
func TestViewerWriterCleansControlCharacters(t *testing.T) {
	dir := knowledgeDir(t)
	writer, err := NewViewerWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	input := viewerTestEntry()
	input.Name = "白饭\n哥\x00"
	input.Summary = "性格：话痨\r\n偏好：猫猫"
	if err := writer.Upsert(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	reader := embeddingReader(t, dir)
	e := findViewerEntry(t, reader, viewerKnowledgeID(input.UID))
	if strings.ContainsAny(e.title+e.usage, "\r\n\x00") {
		t.Fatalf("控制字符未清洗：title=%q usage=%q", e.title, e.usage)
	}
	if !strings.Contains(e.title, "白饭 哥") || !strings.Contains(e.usage, "性格：话痨 偏好：猫猫") {
		t.Fatalf("清洗后正文不符：title=%q usage=%q", e.title, e.usage)
	}
}

func TestViewerWriterInvalidExistingFile(t *testing.T) {
	dir := knowledgeDir(t)
	writeKnowledge(t, dir, "viewers.md", "这不是 YAML")
	writer, err := NewViewerWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Upsert(context.Background(), viewerTestEntry()); err == nil {
		t.Fatal("已有文件不可解析时应拒绝写入")
	}
	data, err := os.ReadFile(filepath.Join(dir, viewerKnowledgeFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "这不是 YAML" {
		t.Fatalf("解析失败时不应改写旧文件：%q", data)
	}
}

// 确保 YAML 字段名与 Reader 约定一致（防止 encodeViewerDoc 漂移）。
func TestViewerDiskEntryWireShape(t *testing.T) {
	doc := diskFile{Schema: "fish-knowledge/v1", Entries: &[]diskEntry{}}
	data, err := encodeViewerDoc(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "---\n") {
		t.Fatal("front matter 起始分隔符缺失")
	}
	var decoded diskFile
	front := strings.SplitN(string(data), "---\n", 3)
	if len(front) < 3 {
		t.Fatalf("front matter 不完整：%q", data)
	}
	if err := yaml.Unmarshal([]byte(front[1]), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schema != "fish-knowledge/v1" || decoded.Entries == nil {
		t.Fatalf("编码后的 schema/entries 不符：%+v", decoded)
	}
}
