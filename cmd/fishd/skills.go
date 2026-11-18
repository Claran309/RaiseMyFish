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
		Desc:     "读取技能说明；args 传技能名，或 JSON {\"name\":\"技能名\",\"file\":\"genres/_index.md\",\"offset\":0,\"limit\":1800} 按需读参考文件（大技能不要一次全读）",
		MaxRunes: 2600,
		Run: func(_ context.Context, args string) (string, error) {
			name, file, offset, limit, err := parseSkillReadArgs(args)
			if err != nil {
				return "", err
			}
			var text string
			if file == "" {
				text, err = registry.Read(name)
			} else {
				text, err = registry.ReadFile(name, file)
			}
			if err != nil {
				return "", err
			}
			return pageSkillText(name, file, text, offset, limit), nil
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

func parseSkillReadArgs(args string) (string, string, int, int, error) {
	raw := strings.TrimSpace(args)
	if raw == "" {
		return "", "", 0, 0, fmt.Errorf("skill_read 需要技能名")
	}
	if !strings.HasPrefix(raw, "{") {
		return raw, "", 0, 0, nil
	}
	var req struct {
		Name   string `json:"name"`
		File   string `json:"file"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return "", "", 0, 0, fmt.Errorf("skill_read 参数不是合法 JSON：%w", err)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return "", "", 0, 0, fmt.Errorf("skill_read 缺少 name")
	}
	if req.Offset < 0 || req.Limit < 0 {
		return "", "", 0, 0, fmt.Errorf("skill_read 的 offset/limit 不能为负")
	}
	if req.Limit > 2400 {
		req.Limit = 2400
	}
	return name, strings.TrimSpace(req.File), req.Offset, req.Limit, nil
}

// pageSkillText 按字符分页，避免一次把大技能正文全部塞进 ReAct prompt。
func pageSkillText(name, file, text string, offset, limit int) string {
	if limit <= 0 {
		limit = 1800
	}
	runes := []rune(text)
	if offset > len(runes) {
		offset = len(runes)
	}
	end := offset + limit
	if end > len(runes) {
		end = len(runes)
	}
	header := "技能=" + name
	if file != "" {
		header += " | 文件=" + file
	}
	header += fmt.Sprintf(" | 字符=%d-%d/%d", offset, end, len(runes))
	out := header + "\n" + string(runes[offset:end])
	if end < len(runes) {
		out += "\n…（还有内容，用 offset 继续读）"
	}
	return out
}
