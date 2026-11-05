package config

import (
	"errors"
	"regexp"
	"strings"
)

// MCP 是 stdio MCP 服务器配置（`[mcp]`），默认关闭。
// 工具白名单为空表示暴露全部已注册工具；副作用工具仍受 [reasoning] allow_side_effect_tools 控制。
type MCP struct {
	Enabled bool `toml:"enabled"`
	// AllowedTools 是精确工具名白名单；非空时只暴露其中列出的工具。
	AllowedTools []string `toml:"allowed_tools"`
}

var mcpToolName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func defaultMCP() MCP { return MCP{Enabled: false} }

// Validate 只校验白名单格式与重复项；工具是否已注册由 fishmcp 装配时判断。
func (m MCP) Validate() error {
	seen := make(map[string]struct{}, len(m.AllowedTools))
	for _, raw := range m.AllowedTools {
		if strings.ContainsAny(raw, "\r\n\t\x00") {
			return errors.New("MCP 工具名不能包含控制字符")
		}
		name := strings.TrimSpace(raw)
		if !mcpToolName.MatchString(name) {
			return errors.New("MCP 工具名只能是小写字母/数字/下划线（且以字母开头）")
		}
		if _, dup := seen[name]; dup {
			return errors.New("MCP 工具白名单存在重复项")
		}
		seen[name] = struct{}{}
	}
	return nil
}
