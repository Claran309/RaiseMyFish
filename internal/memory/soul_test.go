package memory

// 本文件测灵魂记忆：渲染截断、去重、读写往返、空文件。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func soulEntry(title, summary string, imp float64, at time.Time) SoulMemory {
	return SoulMemory{Title: title, Summary: summary, Importance: imp, ValidFrom: at}
}

// TestRenderSoulTruncatesTo500 渲染结果 ≤500 字，超长自动截断。
func TestRenderSoulTruncatesTo500(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var entries []SoulMemory
	for i := 0; i < 30; i++ {
		entries = append(entries, soulEntry(
			strings.Repeat("标题", 3)+string(rune('A'+i)),
			strings.Repeat("很长的摘要内容", 20),
			0.9, base.Add(time.Duration(i)*time.Minute)))
	}
	got := RenderSoul(entries)
	if n := len([]rune(got)); n > SoulMaxRunes+1 {
		t.Fatalf("渲染超长：%d rune", n)
	}
	if got == "" {
		t.Fatal("不应为空")
	}
}

// TestRenderSoulEmpty 空输入 → 空串（不产生空段落）。
func TestRenderSoulEmpty(t *testing.T) {
	if got := RenderSoul(nil); got != "" {
		t.Errorf("空输入应返回空串，实际 %q", got)
	}
	if got := RenderSoul([]SoulMemory{{Title: "  ", Summary: "x"}}); got != "" {
		t.Errorf("空标题应被丢弃，实际 %q", got)
	}
}

// TestSanitizeSoulDedup 同标题去重：保留 importance 高的那条。
func TestSanitizeSoulDedup(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	entries := []SoulMemory{
		soulEntry("主人是学生", "低重要度版本", 0.8, base),
		soulEntry("主人是学生", "高重要度版本", 0.95, base.Add(time.Hour)),
		soulEntry("本鱼是鱼", "独立条目", 0.9, base),
	}
	got := sanitizeSoul(entries)
	if len(got) != 2 {
		t.Fatalf("去重后应 2 条，实际 %d：%+v", len(got), got)
	}
	// 重要度高的排前面，且内容是高重要度版本
	if got[0].Title != "主人是学生" || got[0].Summary != "高重要度版本" {
		t.Errorf("保留的不是高重要度版本：%+v", got[0])
	}
}

// TestSoulWriteAndLoad 写入后能读回，字段一致。
func TestSoulWriteAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "soul-memories.md")
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	want := []SoulMemory{
		soulEntry("主人是学生", "在读大学", 0.9, base),
		soulEntry("主人爱吃辣", "无辣不欢", 0.85, base.Add(time.Minute)),
	}
	if err := WriteSoulAtomic(context.Background(), path, want); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	got, err := LoadSoul(context.Background(), path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(got) != 2 || got[0].Title != "主人是学生" || got[1].Summary != "无辣不欢" {
		t.Fatalf("读回内容不符：%+v", got)
	}
	if !got[0].ValidFrom.Equal(base) {
		t.Errorf("valid_from 未保留：%v", got[0].ValidFrom)
	}
}

// TestLoadSoulMissingFile 文件不存在返回空（不报错）。
func TestLoadSoulMissingFile(t *testing.T) {
	got, err := LoadSoul(context.Background(), filepath.Join(t.TempDir(), "nope.md"))
	if err != nil || len(got) != 0 {
		t.Fatalf("缺失文件应返回空：%+v / %v", got, err)
	}
}

// TestLoadSoulBadSchema schema 不符时报错，避免读到脏文件。
func TestLoadSoulBadSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	if err := os.WriteFile(path, []byte("schema: other/v9\nentries: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSoul(context.Background(), path); err == nil {
		t.Error("schema 不符应报错")
	}
}
