package config

import (
	"errors"
	"strings"
)

// Knowledge 启用只加载不可变Reader快照；不代表事件消费链路已接通。
type Knowledge struct {
	Enabled       bool   `toml:"enabled"`
	Dir           string `toml:"dir" json:"-"`
	BudgetPercent int    `toml:"budget_percent"`
}

func defaultKnowledge() Knowledge {
	return Knowledge{Dir: "personas/dafeiyu/knowledge", BudgetPercent: 10}
}

// Validate 不访问文件，目录检查由显式启用后的装配执行。
func (k Knowledge) Validate() error {
	if k.BudgetPercent < 0 || k.BudgetPercent > 10 {
		return errors.New("知识预算比例必须为0至10的整数")
	}
	if k.Enabled && strings.TrimSpace(k.Dir) == "" {
		return errors.New("知识启用需要非空目录")
	}
	return nil
}
