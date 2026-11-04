package knowledge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"RaiseMyFish/internal/event"
)

const emptyFile = "---\nschema: fish-knowledge/v1\nentries: []\n---\n"
const sampleFile = "---\nschema: fish-knowledge/v1\nentries:\n  - id: first\n    title: 原词\n    channel: [live, comment, dm, qq_private, qq_group]\n    keywords: [别名]\n    tags: [吐槽]\n    priority: 50\n    visibility: public\n    source: test:first\n    summary: 原始解释\n    usage: |-\n      第一行\n        第二行\n---\n维护说明不召回。\n"

func writeKnowledge(t *testing.T, dir, name, text string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func knowledgeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"lore.md", "memes.md", "viewers.md", "games.md"} {
		writeKnowledge(t, dir, name, emptyFile)
	}
	return dir
}

func TestLoadFilesAndRawRevision(t *testing.T) {
	dir := knowledgeDir(t)
	writeKnowledge(t, dir, "memes.md", sampleFile)
	r, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.current().entries) != 1 || !strings.Contains(r.current().entries[0].item.Text, "第一行\n  第二行") || strings.Contains(r.current().entries[0].item.Text, "维护说明") {
		t.Fatal("正文未保留或混入维护说明")
	}
	var input strings.Builder
	input.WriteString("fish-knowledge/v1\n")
	for _, name := range []string{"games.md", "lore.md", "memes.md", "viewers.md"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&input, "%s\t%x\n", name, sha256.Sum256(data))
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(input.String())))
	if r.current().revision != want {
		t.Fatal("Revision不符合原始字节算法")
	}
	other := knowledgeDir(t)
	writeKnowledge(t, other, "memes.md", sampleFile)
	r2, err := NewFileReader(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if r2.current().revision != want {
		t.Fatal("绝对目录污染版本")
	}
	writeKnowledge(t, dir, "memes.md", "\ufeff"+strings.ReplaceAll(sampleFile, "\n", "\r\n"))
	r3, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if r3.current().revision == want || r3.current().entries[0].item.Text != r.current().entries[0].item.Text {
		t.Fatal("原始换行变化未改变版本或改变正文")
	}
}

func TestLoadRejectsMalformed(t *testing.T) {
	tests := map[string]string{
		"未知schema":     strings.Replace(sampleFile, "fish-knowledge/v1", "unknown", 1),
		"缺schema":      strings.Replace(sampleFile, "schema: fish-knowledge/v1\n", "", 1),
		"缺entries":     "---\nschema: fish-knowledge/v1\n---\n",
		"null列表":       strings.Replace(emptyFile, "entries: []", "entries: null", 1),
		"未知字段":         strings.Replace(sampleFile, "priority: 50", "priority: 50\n    extra: secret-marker", 1),
		"重复字段":         strings.Replace(sampleFile, "priority: 50", "priority: 50\n    priority: 60", 1),
		"空ID":          strings.Replace(sampleFile, "id: first", "id: ''", 1),
		"缺title":       strings.Replace(sampleFile, "    title: 原词\n", "", 1),
		"数字title":      strings.Replace(sampleFile, "title: 原词", "title: 123", 1),
		"数字keyword":    strings.Replace(sampleFile, "keywords: [别名]", "keywords: [123]", 1),
		"缺channel":     strings.Replace(sampleFile, "    channel: [live, comment, dm, qq_private, qq_group]\n", "", 1),
		"缺tags":        strings.Replace(sampleFile, "    tags: [吐槽]\n", "", 1),
		"缺priority":    strings.Replace(sampleFile, "    priority: 50\n", "", 1),
		"缺visibility":  strings.Replace(sampleFile, "    visibility: public\n", "", 1),
		"坏visibility":  strings.Replace(sampleFile, "visibility: public", "visibility: PUBLIC", 1),
		"空usage":       strings.Replace(sampleFile, "usage: |-\n      第一行\n        第二行", "usage: ''", 1),
		"私密无来源":        strings.Replace(sampleFile, "visibility: public", "visibility: private", 1),
		"DM伪公开":        strings.Replace(sampleFile, "visibility: public", "visibility: public\n    scope: {channel: dm, session: alice, private: true}", 1),
		"别名锚点":         strings.Replace(sampleFile, "tags: [吐槽]", "tags: &tags [吐槽]", 1),
		"多文档":          sampleFile + "---\nschema: fish-knowledge/v1\nentries: []\n---\n",
		"未闭合":          "---\nschema: fish-knowledge/v1\nentries: []\n",
		"无frontmatter": "schema: fish-knowledge/v1\nentries: []\n",
		"非法UTF8":       string([]byte{255}),
		"文件过大":         strings.Repeat("a", (1<<20)+1),
		"条目过大":         strings.Replace(sampleFile, "原始解释", strings.Repeat("x", 16385), 1),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			dir := knowledgeDir(t)
			writeKnowledge(t, dir, "memes.md", data)
			r, err := NewFileReader(context.Background(), dir)
			if err == nil || r != nil {
				t.Fatal("非法文件未整体拒绝")
			}
			if strings.Contains(err.Error(), "secret-marker") {
				t.Fatal("错误回显正文")
			}
		})
	}
}

func TestLoadRequiredAndBatches(t *testing.T) {
	dir := knowledgeDir(t)
	if err := os.Remove(filepath.Join(dir, "games.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileReader(context.Background(), dir); err == nil {
		t.Fatal("缺必需文件未拒绝")
	}
	writeKnowledge(t, dir, "games.md", emptyFile)
	writeKnowledge(t, dir, "memes.md", sampleFile)
	writeKnowledge(t, dir, "memes/batch-0002.md", sampleFile)
	if _, err := NewFileReader(context.Background(), dir); err == nil {
		t.Fatal("跨文件重复ID未拒绝")
	}
	writeKnowledge(t, dir, "memes/batch-0002.md", strings.Replace(sampleFile, "id: first", "id: second", 1))
	writeKnowledge(t, dir, "other.md", "无关文件不应该读取")
	writeKnowledge(t, dir, "memes/not-batch.md", "无关文件不应该读取")
	r, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.current().entries) != 2 {
		t.Fatal("没有按白名单加载批次")
	}
	if _, err := NewFileReader(nil, dir); err == nil { //nolint:staticcheck // 专门验证非法nil输入的错误路径。
		t.Fatal("nil ctx未拒绝")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewFileReader(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatal("取消错误丢失")
	}
	if _, err := NewFileReader(context.Background(), ""); err == nil {
		t.Fatal("空目录未拒绝")
	}
}

func TestLoadLearnedMemeFile(t *testing.T) {
	dir := knowledgeDir(t)
	writeKnowledge(t, dir, "memes/learned.md", sampleFile)
	r, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Recall(context.Background(), Query{Scope: Scope{Channel: event.ChLive, Session: "room"}, Text: "原词怎么用"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "first" {
		t.Fatalf("learned.md 未参与召回：%+v", got.Items)
	}
	writeKnowledge(t, dir, "memes/learned.md", strings.Repeat("a", maxFileBytes+1))
	if _, err := NewFileReader(context.Background(), dir); err == nil {
		t.Fatal("learned.md 超过单文件上限未拒绝")
	}
}

func TestLoadRejectsEscapingSymlink(t *testing.T) {
	dir, outside := knowledgeDir(t), t.TempDir()
	writeKnowledge(t, outside, "secret.md", sampleFile)
	if err := os.Remove(filepath.Join(dir, "memes.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(dir, "memes.md")); err != nil {
		t.Skip("系统不允许当前用户创建符号链接；Linux/race环境另验")
	}
	if _, err := NewFileReader(context.Background(), dir); err == nil {
		t.Fatal("链接逃逸未拒绝")
	}
}

func TestLoadPrivateAndNonMeme(t *testing.T) {
	dir := knowledgeDir(t)
	private := strings.Replace(sampleFile, "visibility: public", "visibility: private\n    scope: {channel: dm, session: alice, private: true}", 1)
	private = strings.Replace(private, "[live, comment, dm, qq_private, qq_group]", "[dm]", 1)
	writeKnowledge(t, dir, "memes.md", private)
	writeKnowledge(t, dir, "lore.md", strings.Replace(strings.Replace(sampleFile, "id: first", "id: lore", 1), "    usage: |-\n      第一行\n        第二行\n", "", 1))
	if _, err := NewFileReader(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	writeKnowledge(t, dir, "lore.md", strings.Replace(sampleFile, "summary: 原始解释", "summary: ''", 1))
	if _, err := NewFileReader(context.Background(), dir); err == nil {
		t.Fatal("非梗空正文未拒绝")
	}
}

func TestLoadRejectsTotalBytesAndEntryCount(t *testing.T) {
	t.Run("总字节", func(t *testing.T) {
		dir := knowledgeDir(t)
		data := emptyFile + strings.Repeat("x", maxFileBytes-len(emptyFile))
		for i := range 33 {
			writeKnowledge(t, dir, fmt.Sprintf("memes/batch-%04d.md", i), data)
		}
		if r, err := NewFileReader(context.Background(), dir); err == nil || r != nil {
			t.Fatal("总字节上限未拒绝")
		}
	})
	t.Run("总条目", func(t *testing.T) {
		dir := knowledgeDir(t)
		for batch := range 3 {
			var data strings.Builder
			data.WriteString("---\nschema: fish-knowledge/v1\nentries:\n")
			for i := range 3334 {
				fmt.Fprintf(&data, "  - id: item-%d-%d\n    title: 标题\n    channel: [live]\n    tags: []\n    priority: 1\n    visibility: public\n    source: test:one\n    usage: 接法\n", batch, i)
			}
			data.WriteString("---\n")
			name := "memes.md"
			if batch > 0 {
				name = fmt.Sprintf("memes/batch-%04d.md", batch)
			}
			writeKnowledge(t, dir, name, data.String())
		}
		if r, err := NewFileReader(context.Background(), dir); err == nil || r != nil {
			t.Fatal("总条目上限未拒绝")
		}
	})
}

func TestLoadRespondsToCancellationDuringWork(t *testing.T) {
	dir := knowledgeDir(t)
	writeKnowledge(t, dir, "memes.md", sampleFile)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelAfterChecks{Context: base, cancel: cancel}
	if r, err := NewFileReader(ctx, dir); !errors.Is(err, context.Canceled) || r != nil {
		t.Fatal("加载中取消错误丢失")
	}
}
