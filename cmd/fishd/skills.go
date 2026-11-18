package main

// 本文件把本地技能注册表包装成 ReAct 工具。
//
// 三个工具只读/执行技能包，不直接写库、不发消息；脚本执行的安全边界在 skills.Registry。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/skills"
)

const (
	toolSkillList = "skill_list"
	toolSkillRead = "skill_read"
	toolSkillRun  = "skill_run"
)

// buildSkillToolbox 生成技能三件套；注册表为空也注册，让模型知道「当前没有技能」。
func buildSkillToolbox(registry *skills.Registry) (*reasoning.Toolbox, error) {
	if registry == nil {
		return nil, nil
	}
	list := reasoning.Tool{
		Name:     toolSkillList,
		Desc:     "列出当前可用技能的名称与用途；不确定该用哪个技能时先调它",
		MaxRunes: 900,
		Run: func(context.Context, string) (string, error) {
			items := registry.List()
			if len(items) == 0 {
				return "当前没有可用技能", nil
			}
			var sb strings.Builder
			for _, item := range items {
				fmt.Fprintf(&sb, "- %s：%s", item.Name, item.Description)
				if !item.HasScript {
					sb.WriteString("（仅说明）")
				}
				sb.WriteByte('\n')
			}
			return sb.String(), nil
		},
	}
	read := reasoning.Tool{
		Name:     toolSkillRead,
		Desc:     "读取某个技能的完整操作说明；args 传技能名（先用 skill_list 确认）",
		MaxRunes: 1800,
		Run: func(_ context.Context, args string) (string, error) {
			name := strings.TrimSpace(args)
			if name == "" {
				return "", fmt.Errorf("skill_read 需要技能名")
			}
			return registry.Read(name)
		},
	}
	run := reasoning.Tool{
		Name:     toolSkillRun,
		Desc:     "执行技能声明的脚本；args 传 JSON：{\"name\":\"技能名\",\"args\":\"传给脚本的文本\"}；只有带脚本的技能可执行",
		MaxRunes: 900,
		Run: func(ctx context.Context, args string) (string, error) {
			name, payload, err := parseSkillRunArgs(args)
			if err != nil {
				return "", err
			}
			return registry.Run(ctx, name, payload)
		},
	}
	return reasoning.NewToolbox(list, read, run)
}

func parseSkillRunArgs(args string) (string, string, error) {
	raw := strings.TrimSpace(args)
	if raw == "" {
		return "", "", fmt.Errorf("skill_run 需要技能名")
	}
	if strings.HasPrefix(raw, "{") {
		var req struct {
			Name string `json:"name"`
			Args string `json:"args"`
		}
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			return "", "", fmt.Errorf("skill_run 参数不是合法 JSON：%w", err)
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			return "", "", fmt.Errorf("skill_run 缺少 name")
		}
		return name, req.Args, nil
	}
	return raw, "", nil
}
