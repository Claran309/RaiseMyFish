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

// TestMergeSoulKeepsManual 手写条目在重建后保留，且同标题手写优先。
func TestMergeSoulKeepsManual(t *testing.T) {
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	existing := []SoulMemory{
		{Title: "本鱼是鱼", Summary: "手写：说话短", Importance: 0.9, ValidFrom: base, Manual: true},
		{Title: "主人是学生", Summary: "自动旧版", Importance: 0.8, ValidFrom: base},
	}
	generated := []SoulMemory{
		{Title: "主人是学生", Summary: "自动新版", Importance: 0.85, ValidFrom: base.Add(time.Hour)},
		{Title: "主人爱吃辣", Summary: "自动新增", Importance: 0.8, ValidFrom: base},
	}
	got := MergeSoul(existing, generated)

	byTitle := map[string]SoulMemory{}
	for _, e := range got {
		byTitle[e.Title] = e
	}
	if len(got) != 3 {
		t.Fatalf("应 3 条（1 手写 + 2 自动），实际 %d：%+v", len(got), got)
	}
	if m, ok := byTitle["本鱼是鱼"]; !ok || !m.Manual || m.Summary != "手写：说话短" {
		t.Errorf("手写条目丢失或被改：%+v", m)
	}
	if a := byTitle["主人是学生"]; a.Summary != "自动新版" {
		t.Errorf("自动条目未更新：%+v", a)
	}
	if _, ok := byTitle["主人爱吃辣"]; !ok {
		t.Error("自动新增条目丢失")
	}
}

// TestMergeSoulManualWinsOnSameTitle 同标题时手写优先。
func TestMergeSoulManualWinsOnSameTitle(t *testing.T) {
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	existing := []SoulMemory{{Title: "主人是学生", Summary: "手写版", Importance: 0.9, ValidFrom: base, Manual: true}}
	generated := []SoulMemory{{Title: "主人是学生", Summary: "自动版", Importance: 0.95, ValidFrom: base}}
	got := MergeSoul(existing, generated)
	if len(got) != 1 || got[0].Summary != "手写版" {
		t.Fatalf("手写应优先：%+v", got)
	}
}

// TestSoulSubjectFieldsRoundTrip 主体/来源/可见性/发送者写入后读回不丢。
func TestSoulSubjectFieldsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul-memories.md")
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	want := SoulMemory{
		Title: "主人是学生", Summary: "在读大学", Importance: 0.9, ValidFrom: base,
		Subject: SoulSubjectOwner, Source: "owner_direct", Visibility: SoulVisibilityPublic,
		FromID: "1000", FromName: "Claran",
	}
	if err := WriteSoulAtomic(context.Background(), path, []SoulMemory{want}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	got, err := LoadSoul(context.Background(), path)
	if err != nil || len(got) != 1 {
		t.Fatalf("读回失败：%+v / %v", got, err)
	}
	e := got[0]
	if e.Subject != SoulSubjectOwner || e.Source != "owner_direct" ||
		e.Visibility != SoulVisibilityPublic || e.FromID != "1000" || e.FromName != "Claran" {
		t.Fatalf("主体/来源字段丢失：%+v", e)
	}
}

// TestSoulLegacyEntryDefaults 旧文件缺 subject/visibility 时按 fish/public 处理。
func TestSoulLegacyEntryDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	payload := "schema: soul-memories/v1\nentries:\n  - title: 本鱼是鱼\n    summary: 陪聊\n    importance: 0.9\n    valid_from: 2026-11-01T00:00:00Z\n"
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSoul(context.Background(), path)
	if err != nil || len(got) != 1 {
		t.Fatalf("读取失败：%+v / %v", got, err)
	}
	if got[0].Subject != SoulSubjectFish || got[0].Visibility != SoulVisibilityPublic {
		t.Fatalf("旧条目默认值不符：%+v", got[0])
	}
}

// TestRenderSoulOnlyGlobalEntries 观众/私密条目不进全局注入。
func TestRenderSoulOnlyGlobalEntries(t *testing.T) {
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	entries := []SoulMemory{
		{Title: "主人是学生", Summary: "在读大学", Importance: 0.9, ValidFrom: base,
			Subject: SoulSubjectOwner, Visibility: SoulVisibilityPublic},
		{Title: "观众是大三学生", Summary: "某位观众的自述", Importance: 0.95, ValidFrom: base,
			Subject: SoulSubjectViewer, Visibility: SoulVisibilityPublic, FromID: "u-1"},
		{Title: "私密设定", Summary: "只属于私聊", Importance: 0.98, ValidFrom: base,
			Subject: SoulSubjectOwner, Visibility: SoulVisibilityPrivate},
		{Title: "本鱼是直播鱼", Summary: "陪主人聊天", Importance: 0.9, ValidFrom: base,
			Subject: SoulSubjectFish, Visibility: SoulVisibilityPublic},
	}
	got := RenderSoul(entries)
	for _, want := range []string{"主人是学生", "本鱼是直播鱼"} {
		if !strings.Contains(got, want) {
			t.Errorf("全局条目缺失 %q：%q", want, got)
		}
	}
	for _, banned := range []string{"观众是大三学生", "私密设定"} {
		if strings.Contains(got, banned) {
			t.Errorf("非全局条目被注入 %q：%q", banned, got)
		}
	}
}

// TestSanitizeSoulKeepsDifferentSubjects 同标题不同主体不互相去重。
func TestSanitizeSoulKeepsDifferentSubjects(t *testing.T) {
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	got := sanitizeSoul([]SoulMemory{
		{Title: "我是学生", Summary: "主人版", Importance: 0.9, ValidFrom: base, Subject: SoulSubjectOwner},
		{Title: "我是学生", Summary: "观众版", Importance: 0.95, ValidFrom: base, Subject: SoulSubjectViewer, FromID: "u-1"},
	})
	if len(got) != 2 {
		t.Fatalf("不同主体不应去重：%+v", got)
	}
}
