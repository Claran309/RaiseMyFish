package skills

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeSkill(t *testing.T, root, name, extra, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: 测试技能\nversion: 1\n" + extra + "---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newTestRegistry(t *testing.T, root string, allow bool) *Registry {
	t.Helper()
	reg, err := New(Options{Dir: root, AllowScripts: allow, MaxTimeout: time.Minute, MaxOutputBytes: 4096, CheckInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestRegistryLoadsManifestAndBody(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "white-rice", "", "# 白饭\n先接住具体内容。\n")
	reg := newTestRegistry(t, root, false)
	items := reg.List()
	if len(items) != 1 || items[0].Name != "white-rice" || items[0].HasScript {
		t.Fatalf("技能列表不符：%+v", items)
	}
	body, err := reg.Read("white-rice")
	if err != nil || !strings.Contains(body, "先接住具体内容") {
		t.Fatalf("读取正文失败：%q err=%v", body, err)
	}
	if _, ok := reg.Get("missing"); ok {
		t.Fatal("不存在的技能被找到")
	}
}

func TestRegistryRejectsEntryTraversal(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "bad", "entry: ../evil.ps1\n", "# bad\n")
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "evil.ps1"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := New(Options{Dir: root, AllowScripts: true, MaxTimeout: time.Minute, CheckInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(); err == nil {
		t.Fatal("越界 entry 未被拒绝")
	}
	if len(reg.List()) != 0 {
		t.Fatal("坏技能不应进入注册表")
	}
}

func TestRegistryRunArgsAndAllowScripts(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "dice", "entry: scripts/roll.ps1\ntimeout_seconds: 5\n", "# dice\n")
	if err := os.WriteFile(filepath.Join(dir, "scripts", "roll.ps1"), []byte("Write-Output ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := newTestRegistry(t, root, false)
	if _, err := reg.Run(context.Background(), "dice", "2"); err == nil {
		t.Fatal("allow_scripts=false 时脚本仍执行")
	}
	reg = newTestRegistry(t, root, true)
	var gotSkill, gotArgs string
	reg.execute = func(_ context.Context, skill Skill, args string) (string, error) {
		gotSkill, gotArgs = skill.Name, args
		return "ok", nil
	}
	out, err := reg.Run(context.Background(), "dice", "2")
	if err != nil || out != "ok" || gotSkill != "dice" || gotArgs != "2" {
		t.Fatalf("脚本调用参数错误：out=%q err=%v skill=%q args=%q", out, err, gotSkill, gotArgs)
	}
	if _, err := reg.Run(context.Background(), "dice", strings.Repeat("x", maxArgsBytes+1)); err == nil {
		t.Fatal("过大参数未被拒绝")
	}
}

func TestExecuteScriptBatch(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 测 .bat")
	}
	root := t.TempDir()
	dir := writeSkill(t, root, "echo", "entry: scripts/echo.bat\ntimeout_seconds: 5\n", "# echo\n")
	if err := os.WriteFile(filepath.Join(dir, "scripts", "echo.bat"), []byte("@echo off\r\necho 参数=%FISH_SKILL_ARGS%\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := newTestRegistry(t, root, true)
	out, err := reg.Run(context.Background(), "echo", "rice")
	if err != nil || !strings.Contains(out, "参数=rice") {
		t.Fatalf("脚本执行失败：%q err=%v", out, err)
	}
}

func TestSplitFrontmatterRequiresDelimiters(t *testing.T) {
	for _, raw := range []string{"# no front", "---\nname: x\n"} {
		if _, _, err := splitFrontmatter(raw); err == nil {
			t.Fatalf("坏 frontmatter 未被拒绝：%q", raw)
		}
	}
	front, body, err := splitFrontmatter("---\nname: x\n---\n正文\n")
	if err != nil || !strings.Contains(front, "name: x") || body != "正文\n" {
		t.Fatalf("frontmatter 拆分错误：front=%q body=%q err=%v", front, body, err)
	}
}

func TestRegistryReadFile(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "docs-skill", "", "# docs\n")
	ref := filepath.Join(dir, "references")
	if err := os.MkdirAll(ref, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ref, "genre.md"), []byte("题材说明"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := newTestRegistry(t, root, false)
	text, err := reg.ReadFile("docs-skill", "references/genre.md")
	if err != nil || text != "题材说明" {
		t.Fatalf("读取参考文件失败：%q err=%v", text, err)
	}
	for _, bad := range []string{"../secret.md", "references/../../secret.md", "scripts/run.py"} {
		if _, err := reg.ReadFile("docs-skill", bad); err == nil {
			t.Fatalf("非法参考路径未被拒绝：%s", bad)
		}
	}
}

func TestRepositorySkillSamples(t *testing.T) {
	reg, err := New(Options{
		Dir:            filepath.Join("..", "..", "skills"),
		AllowScripts:   true,
		MaxTimeout:     time.Minute,
		MaxOutputBytes: 4096,
		CheckInterval:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(); err != nil {
		t.Fatalf("仓库示例技能加载失败：%v", err)
	}
	names := map[string]bool{}
	for _, item := range reg.List() {
		names[item.Name] = true
	}
	if !names["white-rice"] || !names["dice"] || !names["foreverse-character-card-skills"] || !names["csp"] {
		t.Fatalf("仓库示例技能缺失：%+v", names)
	}
	if runtime.GOOS != "windows" {
		return
	}
	out, err := reg.Run(context.Background(), "dice", "2")
	if err != nil || !strings.Contains(out, "掷了 2 个骰子") {
		t.Fatalf("骰子示例执行失败：%q err=%v", out, err)
	}
}
