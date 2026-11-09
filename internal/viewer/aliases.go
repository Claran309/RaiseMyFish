package viewer

// 本文件实现观众别名与同音组的生产来源：人工维护的 YAML 文件、确认状态与热更新。
//
// 文件格式见 docs/viewer-aliases.md。三个边界：
//   - 只有 status=confirmed 的别名/同音组进入认人候选；candidate 待确认、
//     rejected 作为负例（抑制「同一别名→同一人」的别名/近似/同音候选）；
//   - 每次认人前比对文件 mtime/大小，变化即重建快照（热更新）；
//     解析失败保留旧快照并回调报错，不让坏文件把已有识别能力打没；
//   - 快照不可变：读取方拿到的是切片副本，重载只替换内部指针内容，无锁读安全。

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	aliasSchema       = "fish-viewer-aliases/v1"
	maxAliasFileBytes = 1 << 20
	maxAliasEntries   = 2000
	maxAliasRunes     = 64
	maxPhoneticWords  = 32
)

// AliasStatus 是别名/同音组的人工确认状态。
type AliasStatus string

const (
	// AliasCandidate 是待确认候选：留档但不参与认人。
	AliasCandidate AliasStatus = "candidate"
	// AliasConfirmed 是已确认：参与别名/同音匹配。
	AliasConfirmed AliasStatus = "confirmed"
	// AliasRejected 是已拒绝：作为负例，抑制同一别名指向该档案的弱证据。
	AliasRejected AliasStatus = "rejected"
)

// 别名来源类型（仅用于标注与人工维护，不改变匹配档位）。
const (
	AliasKindManual     = "manual"
	AliasKindNickname   = "nickname"
	AliasKindHistorical = "historical"
	AliasKindTypo       = "typo"
	AliasKindPhonetic   = "phonetic"
)

// Alias 是一条人工维护的别名：同一个人可以被多个写法指向。
//
// 同一个别名可以对应多个人（不强制唯一），此时会保留多候选而不是二选一。
type Alias struct {
	// Alias 是别名原文（如「弓长三溢」「长弓三亿」）。
	Alias string
	// UID 非空时按 UID 归属；为空时按 Name 归属。
	UID string
	// Name 是归属人的当前昵称（UID 为空时使用）。
	Name string
	// Kind 标注来源：manual/nickname/historical/typo/phonetic；空视作 manual。
	Kind string
	// Status 是确认状态；空视作 confirmed（程序内直接构造的旧用法）。
	Status AliasStatus
}

// AliasIndex 是一次认人使用的不可变别名快照。
type AliasIndex struct {
	// Confirmed 参与别名精确匹配。
	Confirmed []Alias
	// Rejected 是人工拒绝的组合，只用于抑制弱身份候选。
	Rejected []Alias
	// Phonetic 返回同音写法（不含查询词自身）；nil 表示没有同音组数据。
	Phonetic func(string) []string
}

// AliasSource 提供可热更新的别名快照；注入 SearchOptions 后优先于静态表。
type AliasSource interface {
	AliasIndex() AliasIndex
}

// aliasFileDoc 是 YAML 文件的严格结构（未知字段由 KnownFields 拒绝）。
type aliasFileDoc struct {
	Schema         string          `yaml:"schema"`
	Aliases        []aliasDocEntry `yaml:"aliases"`
	PhoneticGroups []phoneticGroup `yaml:"phonetic_groups"`
}

type aliasDocEntry struct {
	Alias  string      `yaml:"alias"`
	UID    string      `yaml:"uid"`
	Name   string      `yaml:"name"`
	Kind   string      `yaml:"kind"`
	Status AliasStatus `yaml:"status"`
}

type phoneticGroup struct {
	Words  []string    `yaml:"words"`
	Status AliasStatus `yaml:"status"`
}

// AliasFile 是别名文件的热更新视图。
//
// 构造不要求文件存在：文件后补、改名、删了再加都能在后续查询时生效。
type AliasFile struct {
	path    string
	onError func(error)

	mu     sync.Mutex
	stamp  aliasFileStamp
	loaded bool
	index  AliasIndex
}

type aliasFileStamp struct {
	modTime time.Time
	size    int64
}

// NewAliasFile 绑定一个别名文件路径；onError 可为 nil（失败只保留旧快照）。
func NewAliasFile(path string, onError func(error)) (*AliasFile, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("观众别名文件路径不能为空")
	}
	return &AliasFile{path: path, onError: onError}, nil
}

// AliasIndex 返回当前快照；文件 mtime/大小变化时先重载。
func (f *AliasFile) AliasIndex() AliasIndex {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloadLocked()
	return cloneAliasIndex(f.index)
}

func cloneAliasIndex(idx AliasIndex) AliasIndex {
	out := AliasIndex{Phonetic: idx.Phonetic}
	if len(idx.Confirmed) > 0 {
		out.Confirmed = append([]Alias(nil), idx.Confirmed...)
	}
	if len(idx.Rejected) > 0 {
		out.Rejected = append([]Alias(nil), idx.Rejected...)
	}
	return out
}

// reloadLocked 按文件指纹决定是否重读；失败保留旧快照。
//
// 先记指纹再解析：坏文件不会被每次查询重复读；下次编辑改变 mtime/大小后再试。
func (f *AliasFile) reloadLocked() {
	info, err := os.Stat(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件暂不存在：保留旧快照，等它出现；已有零指纹时不重复处理。
			if f.loaded && f.stamp == (aliasFileStamp{}) {
				return
			}
			f.stamp, f.loaded = aliasFileStamp{}, true
			return
		}
		f.report(fmt.Errorf("读取观众别名文件失败：%w", err))
		return
	}
	stamp := aliasFileStamp{modTime: info.ModTime(), size: info.Size()}
	if f.loaded && stamp == f.stamp {
		return
	}
	f.stamp, f.loaded = stamp, true
	if info.Size() > maxAliasFileBytes {
		f.report(fmt.Errorf("观众别名文件超过 %d 字节", maxAliasFileBytes))
		return
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		f.report(fmt.Errorf("读取观众别名文件失败：%w", err))
		return
	}
	idx, err := parseAliasFile(data)
	if err != nil {
		f.report(err)
		return
	}
	f.index = idx
}

func (f *AliasFile) report(err error) {
	if f.onError != nil {
		f.onError(err)
	}
}

// parseAliasFile 解析并校验别名文件；错误信息只描述结构问题，不回显文件正文。
func parseAliasFile(data []byte) (AliasIndex, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var doc aliasFileDoc
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return AliasIndex{}, errors.New("观众别名文件为空")
		}
		return AliasIndex{}, errors.New("观众别名文件不是有效的 YAML")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return AliasIndex{}, errors.New("观众别名文件只能包含一个 YAML 文档")
	}
	if doc.Schema != aliasSchema {
		return AliasIndex{}, fmt.Errorf("观众别名文件 schema 必须为 %s", aliasSchema)
	}
	if len(doc.Aliases)+len(doc.PhoneticGroups) > maxAliasEntries {
		return AliasIndex{}, fmt.Errorf("观众别名条目超过上限 %d", maxAliasEntries)
	}

	idx := AliasIndex{}
	seen := make(map[string]bool, len(doc.Aliases))
	for i, entry := range doc.Aliases {
		alias, err := normalizeAliasEntry(entry)
		if err != nil {
			return AliasIndex{}, fmt.Errorf("第 %d 条别名无效：%w", i+1, err)
		}
		key := alias.Alias + "\x00" + alias.UID + "\x00" + normalizeIdentity(alias.Name)
		if seen[key] {
			return AliasIndex{}, fmt.Errorf("第 %d 条别名重复", i+1)
		}
		seen[key] = true
		switch alias.Status {
		case AliasConfirmed:
			idx.Confirmed = append(idx.Confirmed, alias)
		case AliasRejected:
			idx.Rejected = append(idx.Rejected, alias)
		case AliasCandidate:
			// 待确认：留档但不注入。
		}
	}

	phonetic, err := buildPhonetic(doc.PhoneticGroups)
	if err != nil {
		return AliasIndex{}, err
	}
	idx.Phonetic = phonetic
	return idx, nil
}

// normalizeAliasEntry 做结构性校验：目标唯一、UID 合法、状态/来源枚举有效。
func normalizeAliasEntry(entry aliasDocEntry) (Alias, error) {
	alias := strings.TrimSpace(entry.Alias)
	if alias == "" {
		return Alias{}, errors.New("alias 不能为空")
	}
	if utf8.RuneCountInString(alias) > maxAliasRunes || strings.ContainsRune(alias, '\x00') {
		return Alias{}, fmt.Errorf("alias 必须是 %d 字以内的有效文本", maxAliasRunes)
	}
	if normalizeIdentity(alias) == "" {
		return Alias{}, errors.New("alias 必须包含文字")
	}
	uid := strings.TrimSpace(entry.UID)
	name := strings.TrimSpace(entry.Name)
	if (uid == "") == (name == "") {
		return Alias{}, errors.New("uid 与 name 必须且只能填一个")
	}
	if uid != "" {
		if _, ok := parseUID(uid); !ok {
			return Alias{}, errors.New("uid 必须是纯数字平台 UID")
		}
	}
	if name != "" && (utf8.RuneCountInString(name) > maxAliasRunes || strings.ContainsRune(name, '\x00')) {
		return Alias{}, fmt.Errorf("name 必须是 %d 字以内的有效文本", maxAliasRunes)
	}
	if name != "" && normalizeIdentity(name) == "" {
		return Alias{}, errors.New("name 必须包含文字")
	}
	kind := strings.TrimSpace(entry.Kind)
	if kind == "" {
		kind = AliasKindManual
	}
	switch kind {
	case AliasKindManual, AliasKindNickname, AliasKindHistorical, AliasKindTypo, AliasKindPhonetic:
	default:
		return Alias{}, errors.New("kind 必须是 manual/nickname/historical/typo/phonetic")
	}
	switch entry.Status {
	case AliasCandidate, AliasConfirmed, AliasRejected:
	default:
		return Alias{}, errors.New("status 必须是 candidate/confirmed/rejected")
	}
	return Alias{Alias: alias, UID: uid, Name: name, Kind: kind, Status: entry.Status}, nil
}

// buildPhonetic 由已确认同音组构建「词 → 同音写法」查询表。
//
// 同音组只用于扩大近似候选（TierNear/phonetic），不产生别名档，
// 也不自动改写或持久化任何昵称。
func buildPhonetic(groups []phoneticGroup) (func(string) []string, error) {
	table := make(map[string][]string)
	for i, group := range groups {
		switch group.Status {
		case AliasCandidate, AliasConfirmed, AliasRejected:
		default:
			return nil, fmt.Errorf("第 %d 个同音组 status 必须是 candidate/confirmed/rejected", i+1)
		}
		if len(group.Words) < 2 || len(group.Words) > maxPhoneticWords {
			return nil, fmt.Errorf("第 %d 个同音组必须包含 2 至 %d 个词", i+1, maxPhoneticWords)
		}
		words := make([]string, 0, len(group.Words))
		seen := make(map[string]bool, len(group.Words))
		for _, word := range group.Words {
			word = strings.TrimSpace(word)
			norm := normalizeIdentity(word)
			if norm == "" || utf8.RuneCountInString(word) > maxAliasRunes || strings.ContainsRune(word, '\x00') {
				return nil, fmt.Errorf("第 %d 个同音组包含无效词", i+1)
			}
			if seen[norm] {
				return nil, fmt.Errorf("第 %d 个同音组包含重复词", i+1)
			}
			seen[norm] = true
			words = append(words, word)
		}
		if group.Status != AliasConfirmed {
			// 待确认/已拒绝的同音组不参与扩展。
			continue
		}
		for _, word := range words {
			norm := normalizeIdentity(word)
			for _, other := range words {
				if other == word {
					continue
				}
				table[norm] = append(table[norm], other)
			}
		}
	}
	if len(table) == 0 {
		return nil, nil
	}
	return func(word string) []string {
		variants := table[normalizeIdentity(word)]
		if len(variants) == 0 {
			return nil
		}
		return append([]string(nil), variants...)
	}, nil
}
