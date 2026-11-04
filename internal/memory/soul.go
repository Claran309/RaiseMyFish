package memory

// 本文件是「灵魂记忆」的存储格式与读写：长期设定，随 system prompt 注入。
//
// 格式沿用 K0 的 YAML 风格（schema + entries），但独立成文件，
// 不进 knowledge 的召回索引——它是每轮都注入的短设定，不是检索语料。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	// SoulSchema 是文件 schema 标识。
	SoulSchema = "soul-memories/v1"
	// SoulMaxRunes 是注入片段的长度上限（派发单：≤500 字）。
	SoulMaxRunes = 500
	// soulentryMaxRunes 是单条摘要上限，防止一条吃掉整个预算。
	soulEntryMaxRunes = 120
)

// SoulMemory 是一条长期设定。
type SoulMemory struct {
	Title      string    `yaml:"title"`
	Summary    string    `yaml:"summary"`
	Importance float64   `yaml:"importance"`
	ValidFrom  time.Time `yaml:"valid_from"`
}

type soulFile struct {
	Schema  string       `yaml:"schema"`
	Entries []SoulMemory `yaml:"entries"`
}

// LoadSoul 读取灵魂记忆文件；文件不存在返回空切片（不是错误）。
func LoadSoul(ctx context.Context, path string) ([]SoulMemory, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("灵魂记忆路径不能为空")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取灵魂记忆：%w", err)
	}
	var f soulFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, errors.New("灵魂记忆不是合法YAML")
	}
	if f.Schema != "" && f.Schema != SoulSchema {
		return nil, fmt.Errorf("灵魂记忆 schema 不符：%s", f.Schema)
	}
	return sanitizeSoul(f.Entries), nil
}

// WriteSoulAtomic 原子替换文件（同目录临时文件 + rename）。
func WriteSoulAtomic(ctx context.Context, path string, entries []SoulMemory) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("灵魂记忆路径不能为空")
	}
	cleaned := sanitizeSoul(entries)
	payload, err := yaml.Marshal(soulFile{Schema: SoulSchema, Entries: cleaned})
	if err != nil {
		return errors.New("序列化灵魂记忆失败")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建灵魂记忆目录：%w", err)
	}
	tmp, err := os.CreateTemp(dir, ".soul-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件：%w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入灵魂记忆：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("刷盘灵魂记忆：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件：%w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换灵魂记忆：%w", err)
	}
	return nil
}

// RenderSoul 把条目渲染成注入片段（≤500 字）；空输入返回空串。
func RenderSoul(entries []SoulMemory) string {
	cleaned := sanitizeSoul(entries)
	if len(cleaned) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, e := range cleaned {
		line := "- " + e.Title
		if e.Summary != "" && e.Summary != e.Title {
			line += "：" + e.Summary
		}
		line = truncateRunes(line, soulEntryMaxRunes)
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(line)
		if utf8.RuneCountInString(sb.String()) >= SoulMaxRunes {
			break
		}
	}
	return truncateRunes(sb.String(), SoulMaxRunes)
}

// NormalizeSoul 对外暴露的去重排序（写入前用同一套规则数条数）。
func NormalizeSoul(entries []SoulMemory) []SoulMemory { return sanitizeSoul(entries) }

// sanitizeSoul 去空、去重（同 title 取 importance 高的）、按重要度与时间排序。
func sanitizeSoul(entries []SoulMemory) []SoulMemory {
	seen := make(map[string]int, len(entries))
	out := make([]SoulMemory, 0, len(entries))
	for _, e := range entries {
		title := strings.TrimSpace(e.Title)
		summary := strings.TrimSpace(e.Summary)
		if title == "" || summary == "" {
			continue
		}
		e.Title, e.Summary = truncateRunes(title, 60), truncateRunes(summary, soulEntryMaxRunes)
		if e.Importance < 0 {
			e.Importance = 0
		}
		if e.Importance > 1 {
			e.Importance = 1
		}
		if idx, ok := seen[e.Title]; ok {
			// 同标题：保留重要的那条（importance 相同则保留更新的）
			if e.Importance > out[idx].Importance || (e.Importance == out[idx].Importance && e.ValidFrom.After(out[idx].ValidFrom)) {
				out[idx] = e
			}
			continue
		}
		seen[e.Title] = len(out)
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Importance != out[j].Importance {
			return out[i].Importance > out[j].Importance
		}
		return out[i].ValidFrom.After(out[j].ValidFrom)
	})
	return out
}

// checkContext 是本地小工具（memory 包内多处需要）。
func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("记忆操作ctx不能为空")
	}
	return ctx.Err()
}
