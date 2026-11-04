package knowledge

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var _ Reader = (*FileReader)(nil)

type matchScore struct{ tier, longest, count int }
type hit struct {
	entry entry
	score matchScore
}

func normalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func hasWord(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func asciiWord(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' }

// phraseMatch 只为英文词端点加边界，不把中文相邻字误当英文词的一部分。
func phraseMatch(text, word string) bool {
	if word == "" || !hasWord(word) {
		return false
	}
	for offset := 0; offset <= len(text)-len(word); {
		i := strings.Index(text[offset:], word)
		if i < 0 {
			return false
		}
		i += offset
		end := i + len(word)
		left := !asciiWord(word[0]) || i == 0 || !asciiWord(text[i-1])
		right := !asciiWord(word[len(word)-1]) || end == len(text) || !asciiWord(text[end])
		if left && right {
			return true
		}
		offset = i + 1
	}
	return false
}

func genericTag(tag string) bool {
	switch tag {
	case "梗", "知识", "游戏", "设定", "观众", "文字梗", "游戏梗", "视频梗", "图像梗", "短视频梗", "经典梗", "网络迷因", "网络用语", "网络流行语", "社交媒体", "表情包", "二次创作", "弹幕文化", "直播", "弹幕", "评论", "文字", "短视频", "搞笑视频", "自学习", "年度词汇", "2026热词", "meme", "memes", "knowledge", "game", "games":
		return true
	default:
		return false
	}
}

// 召回匹配档位：标题 > 触发词 > 标签 > 资料正文。
// 同一条目命中多个字段时取最高档，同一词组只计一次。
const (
	tierTitle   = 4
	tierKeyword = 3
	tierTag     = 2
	tierSummary = 1
)

const (
	// 资料正文太长，只认至少 3 个字的片段，避免“什么/这个”把无关资料全拉进候选。
	summaryMinRunes = 3
	// 超长查询不做资料档切分，避免按标点切出海量片段放大扫描。
	maxSummaryQueryBytes = 4 << 10
)

func clauseBreak(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }

// summaryMatch 返回资料正文命中的最长片段字数；0 表示未命中。
// 资料正文是长段文字，不能要求整段出现在查询里，因此反过来找
// “查询本身或按标点/空白切出的片段”是否出现在资料中。
func summaryMatch(text, summary string) int {
	if summary == "" || len(text) > maxSummaryQueryBytes {
		return 0
	}
	textLen := utf8.RuneCountInString(text)
	if textLen < summaryMinRunes {
		return 0
	}
	if phraseMatch(summary, text) {
		return textLen
	}
	longest := 0
	for _, part := range strings.FieldsFunc(text, clauseBreak) {
		n := utf8.RuneCountInString(part)
		if n < summaryMinRunes || n <= longest {
			continue
		}
		if phraseMatch(summary, part) {
			longest = n
		}
	}
	return longest
}

func scoreEntry(ctx context.Context, text string, e entry) (matchScore, error) {
	var result matchScore
	seen := make(map[string]bool)
	groups := []struct {
		words []string
		tier  int
	}{
		{[]string{e.title}, tierTitle},
		{e.keywords, tierKeyword},
		{e.tags, tierTag},
		{[]string{e.summary}, tierSummary},
	}
	for _, group := range groups {
		for _, raw := range group.words {
			if err := checkContext(ctx); err != nil {
				return matchScore{}, err
			}
			word := normalize(raw)
			if seen[word] || group.tier == tierTag && genericTag(word) {
				continue
			}
			seen[word] = true
			matched := 0
			if group.tier == tierSummary {
				matched = summaryMatch(text, word)
			} else if phraseMatch(text, word) {
				matched = utf8.RuneCountInString(word)
			}
			if matched == 0 {
				continue
			}
			if group.tier > result.tier {
				result.tier = group.tier
			}
			if matched > result.longest {
				result.longest = matched
			}
			result.count++
		}
	}
	return result, nil
}

func cloneItem(item Item) Item {
	item.Scenes = append([]string(nil), item.Scenes...)
	if item.Origin != nil {
		origin := *item.Origin
		item.Origin = &origin
	}
	return item
}

// Recall 先查权限再匹配，返回独立副本；快照构造后不再接触文件系统。
func (r *FileReader) Recall(ctx context.Context, query Query) (Context, error) {
	text, st, err := r.prepare(ctx, query)
	if err != nil {
		return Context{}, err
	}
	result := Context{Scope: query.Scope, Revision: st.revision}
	if !hasWord(text) {
		return result, nil
	}
	hits, err := r.keywordHits(ctx, text, st, query.Scope)
	if err != nil {
		return Context{}, err
	}
	if err := checkContext(ctx); err != nil {
		return Context{}, err
	}
	sortHits(hits)
	return contextFromHits(query.Scope, st.revision, hits), nil
}

// prepare 统一做 Reader 非空、Scope、UTF-8 与长度校验，并返回归一化文本与当前快照。
func (r *FileReader) prepare(ctx context.Context, query Query) (string, *snapshotState, error) {
	if err := checkContext(ctx); err != nil {
		return "", nil, err
	}
	if r == nil {
		return "", nil, errors.New("知识Reader不能为空")
	}
	if err := ValidateScope(query.Scope); err != nil {
		return "", nil, err
	}
	if !utf8.ValidString(query.Text) {
		return "", nil, errors.New("知识查询不是UTF-8")
	}
	if len(query.Text) > 4<<20 {
		return "", nil, errors.New("知识查询超过字节上限")
	}
	return normalize(query.Text), r.current(), nil
}

// keywordHits 返回所有范围内的关键词命中，不做截断，供纯关键词与混合召回共用。
func (r *FileReader) keywordHits(ctx context.Context, text string, st *snapshotState, scope Scope) ([]hit, error) {
	hits := make([]hit, 0, 3)
	for _, e := range st.entries {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		if !inScope(e.item, scope) {
			continue
		}
		score, err := scoreEntry(ctx, text, e)
		if err != nil {
			return nil, err
		}
		if score.tier > 0 {
			hits = append(hits, hit{entry: e, score: score})
		}
	}
	return hits, nil
}

func sortHits(hits []hit) {
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.score.tier != b.score.tier {
			return a.score.tier > b.score.tier
		}
		if a.score.longest != b.score.longest {
			return a.score.longest > b.score.longest
		}
		if a.score.count != b.score.count {
			return a.score.count > b.score.count
		}
		if a.entry.item.Priority != b.entry.item.Priority {
			return a.entry.item.Priority > b.entry.item.Priority
		}
		return a.entry.item.ID < b.entry.item.ID
	})
}

func contextFromHits(scope Scope, revision string, hits []hit) Context {
	result := Context{Scope: scope, Revision: revision}
	for i := 0; i < len(hits) && i < 3; i++ {
		result.Items = append(result.Items, cloneItem(hits[i].entry.item))
	}
	return result
}
