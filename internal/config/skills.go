package config

import (
	"errors"
	"regexp"
	"strings"
)

// Skills 是本地技能包系统（`[skills]`），默认关闭。
// 技能是文件驱动的：每个子目录一个 SKILL.md，可选脚本入口；不新增代码依赖。
type Skills struct {
	Enabled        bool     `toml:"enabled"`
	Dir            string   `toml:"dir"`
	AllowScripts   bool     `toml:"allow_scripts"`
	CheckSeconds   int      `toml:"check_seconds"`
	MaxTimeoutSec  int      `toml:"max_timeout_seconds"`
	MaxOutputBytes int      `toml:"max_output_bytes"`
	Allowed        []string `toml:"allowed"`
}

var skillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func defaultSkills() Skills {
	return Skills{
		Dir:            "skills",
		CheckSeconds:   30,
		MaxTimeoutSec:  30,
		MaxOutputBytes: 8000,
	}
}

// Validate 只做静态校验；技能文件本身在加载时逐条校验。
func (s Skills) Validate() error {
	if !s.Enabled {
		return nil
	}
	if strings.TrimSpace(s.Dir) == "" || strings.ContainsAny(s.Dir, "\r\n\x00") {
		return errors.New("技能目录不能为空且不能含控制字符")
	}
	if s.CheckSeconds < 1 || s.CheckSeconds > 3600 {
		return errors.New("技能检查间隔必须在1至3600秒之间")
	}
	if s.MaxTimeoutSec < 1 || s.MaxTimeoutSec > 600 {
		return errors.New("技能最大超时必须为1至600秒")
	}
	if s.MaxOutputBytes < 512 || s.MaxOutputBytes > 1<<20 {
		return errors.New("技能输出上限必须在512字节至1MB之间")
	}
	for _, name := range s.Allowed {
		if !skillNamePattern.MatchString(name) {
			return errors.New("技能白名单名称只能是小写字母/数字/短横线")
		}
	}
	return nil
}
