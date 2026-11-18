package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/skills"
	"go.uber.org/zap"
)

func TestInstalledSkillRegistryList(t *testing.T) {
	reg, err := skills.New(skills.Options{
		Dir:            filepath.Join("..", "..", "skills"),
		MaxTimeout:     time.Minute,
		MaxOutputBytes: 4096,
		CheckInterval:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(); err != nil {
		t.Fatalf("项目技能目录加载失败：%v", err)
	}
	box, err := buildSkillToolbox(reg)
	if err != nil {
		t.Fatal(err)
	}
	list, err := box.Run(context.Background(), "skill_list", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"white-rice", "dice", "foreverse-character-card-skills", "csp"} {
		if !strings.Contains(list, want) {
			t.Errorf("skill_list 缺少 %s：%s", want, list)
		}
	}
	if got := len([]rune(list)); got > 900 {
		t.Fatalf("skill_list 超上下文预算：%d 字", got)
	}
	page, err := box.Run(context.Background(), "skill_read", `{"name":"foreverse-character-card-skills","file":"references/chat-quality-doctor/SKILL.md","offset":0,"limit":200}`)
	if err != nil || !strings.Contains(page, "聊后调优") {
		t.Fatalf("参考文件分页读取失败：%q err=%v", page, err)
	}
}

func TestBuildToolExecutorsWiresSkills(t *testing.T) {
	cfg := config.Config{Skills: config.Skills{
		Enabled:        true,
		Dir:            filepath.Join("..", "..", "skills"),
		CheckSeconds:   30,
		MaxTimeoutSec:  30,
		MaxOutputBytes: 8000,
	}}
	execs := buildToolExecutors(cfg, nil, zap.NewNop())
	if execs.Skills == nil {
		t.Fatal("buildToolExecutors 未装配技能注册表")
	}
	names := map[string]bool{}
	for _, item := range execs.Skills.List() {
		names[item.Name] = true
	}
	if !names["foreverse-character-card-skills"] || !names["csp"] {
		t.Fatalf("技能注册表缺少已安装技能：%+v", names)
	}
}

func TestBuildSkillToolboxListAndRead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "white-rice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: white-rice\ndescription: 白饭接梗\nversion: 1\n---\n# 白饭\n先接住内容。\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := skills.New(skills.Options{Dir: root, MaxTimeout: time.Minute, CheckInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(); err != nil {
		t.Fatal(err)
	}
	box, err := buildSkillToolbox(reg)
	if err != nil {
		t.Fatal(err)
	}
	list, err := box.Run(context.Background(), "skill_list", "")
	if err != nil || !strings.Contains(list, "white-rice") || !strings.Contains(list, "白饭接梗") {
		t.Fatalf("skill_list 输出不符：%q err=%v", list, err)
	}
	body, err := box.Run(context.Background(), "skill_read", "white-rice")
	if err != nil || !strings.Contains(body, "先接住内容") {
		t.Fatalf("skill_read 输出不符：%q err=%v", body, err)
	}
	if _, err := box.Run(context.Background(), "skill_read", "missing"); err == nil {
		t.Fatal("不存在的技能未报错")
	}
	if _, _, err := parseSkillRunArgs(`{"name":"white-rice","args":"少来点"}`); err != nil {
		t.Fatalf("skill_run JSON 解析失败：%v", err)
	}
	if name, payload, err := parseSkillRunArgs("white-rice"); err != nil || name != "white-rice" || payload != "" {
		t.Fatalf("skill_run 裸名称解析失败：%q %q %v", name, payload, err)
	}
	name, file, offset, limit, err := parseSkillReadArgs(`{"name":"white-rice","file":"references/genre.md","offset":10,"limit":2000}`)
	if err != nil || name != "white-rice" || file != "references/genre.md" || offset != 10 || limit != 2000 {
		t.Fatalf("skill_read 参数解析失败：%q %q %d %d %v", name, file, offset, limit, err)
	}
	page := pageSkillText("white-rice", "references/genre.md", strings.Repeat("鱼", 100), 10, 20)
	if !strings.Contains(page, "字符=10-30/100") || !strings.Contains(page, "还有内容") {
		t.Fatalf("skill_read 分页不符合预期：%q", page)
	}
}
