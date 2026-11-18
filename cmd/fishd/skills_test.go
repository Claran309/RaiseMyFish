package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/skills"
)

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
}
