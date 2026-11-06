package memory

// 本文件是「灵魂记忆」的存储格式与读写：长期设定，随 system prompt 注入。
//
// 格式沿用 K0 的 YAML 风格（schema + entries），但独立成文件，
// 不进 knowledge 的召回索引——它是每轮都注入的短设定，不是检索语料。
// M12 起条目带主体（fish/owner/viewer）与来源、可见性；全局注入只取
// fish/owner 的公开条目，观众（viewer）与私密条目不得进入 system prompt。

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
	// soulFieldMaxRunes 是来源/主体标识的长度上限，防止异常字段撑爆文件。
	soulFieldMaxRunes = 120

	// SoulSubjectFish 是本鱼自身设定。
	SoulSubjectFish = "fish"
	// SoulSubjectOwner 是主人设定。
	SoulSubjectOwner = "owner"
	// SoulSubjectViewer 是单个观众的设定，不进入全局注入。
	SoulSubjectViewer = "viewer"

	// SoulVisibilityPublic 是公开条目，可进入全局注入。
	SoulVisibilityPublic = "public"
	// SoulVisibilityPrivate 是私密条目，不进入全局注入。
	SoulVisibilityPrivate = "private"
)

// SoulMemory 是一条长期设定。
type SoulMemory struct {
	Title      string    `yaml:"title"`
	Summary    string    `yaml:"summary"`
	Importance float64   `yaml:"importance"`
	ValidFrom  time.Time `yaml:"valid_from"`
	// Subject 是条目主体（fish/owner/viewer）；viewer 不进入全局注入。
	Subject string `yaml:"subject,omitempty"`
	// Source 是来源引用（如 persona/owner_direct），不写本机绝对路径。
	Source string `yaml:"source,omitempty"`
	// Visibility 是可见性（public/private）；private 不进入全局注入。
	Visibility string `yaml:"visibility,omitempty"`
	// FromID/FromName 是事实来源主体（说话人），转换时不能丢。
	FromID   string `yaml:"from_id,omitempty"`
	FromName string `yaml:"from_name,omitempty"`
	// Manual 标记「人写的条目」：重建时不会被自动条目冲掉。
	Manual bool `yaml:"manual,omitempty"`
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
//
// 只渲染全局设定：viewer（单个观众）与 private（私密）条目一律跳过，
// 即使文件里混入了这类条目，也不会进入 system prompt。
func RenderSoul(entries []SoulMemory) string {
	cleaned := sanitizeSoul(entries)
	if len(cleaned) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, e := range cleaned {
		if !injectableSoul(e) {
			continue
		}
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

// injectableSoul 判断条目能否进入全局 system prompt：只允许 fish/owner 的公开条目。
//
// 未知主体或未知可见性按 fail-closed 处理，不注入。
func injectableSoul(e SoulMemory) bool {
	if e.Visibility != "" && e.Visibility != SoulVisibilityPublic {
		return false
	}
	switch e.Subject {
	case "", SoulSubjectFish, SoulSubjectOwner:
		return true
	default:
		return false
	}
}

// MergeSoul 把自动条目合并到已有条目上，**保留手写条目**。
//
// 规则：手写条目永远保留（同标题时手写优先），自动条目整体替换为本次生成的。
func MergeSoul(existing, generated []SoulMemory) []SoulMemory {
	out := make([]SoulMemory, 0, len(existing)+len(generated))
	manual := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		if !e.Manual {
			continue
		}
		out = append(out, e)
		manual[e.Title] = struct{}{}
	}
	for _, e := range generated {
		if _, ok := manual[e.Title]; ok {
			continue // 手写优先
		}
		out = append(out, e)
	}
	return sanitizeSoul(out)
}

// NormalizeSoul 对外暴露的去重排序（写入前用同一套规则数条数）。
func NormalizeSoul(entries []SoulMemory) []SoulMemory { return sanitizeSoul(entries) }

// sanitizeSoul 去空、去重（同主体同标题取 importance 高的）、按重要度与时间排序。
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
		e.Subject = strings.ToLower(strings.TrimSpace(e.Subject))
		if e.Subject == "" {
			e.Subject = SoulSubjectFish
		}
		e.Visibility = strings.ToLower(strings.TrimSpace(e.Visibility))
		if e.Visibility == "" {
			e.Visibility = SoulVisibilityPublic
		}
		e.Source = truncateRunes(strings.TrimSpace(e.Source), soulFieldMaxRunes)
		e.FromID = truncateRunes(strings.TrimSpace(e.FromID), soulFieldMaxRunes)
		e.FromName = truncateRunes(strings.TrimSpace(e.FromName), soulFieldMaxRunes)
		if e.Importance < 0 {
			e.Importance = 0
		}
		if e.Importance > 1 {
			e.Importance = 1
		}
		key := e.Subject + "\x00" + e.Title
		if idx, ok := seen[key]; ok {
			// 同主体同标题：保留重要的那条（importance 相同则保留更新的）
			if e.Importance > out[idx].Importance || (e.Importance == out[idx].Importance && e.ValidFrom.After(out[idx].ValidFrom)) {
				out[idx] = e
			}
			continue
		}
		seen[key] = len(out)
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
