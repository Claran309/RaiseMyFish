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

func scoreEntry(ctx context.Context, text string, e entry) (matchScore, error) {
	var result matchScore
	seen := make(map[string]bool)
	groups := []struct {
		words []string
		tier  int
	}{{[]string{e.title}, 3}, {e.keywords, 2}, {e.tags, 1}}
	for _, group := range groups {
		for _, raw := range group.words {
			if err := checkContext(ctx); err != nil {
				return matchScore{}, err
			}
			word := normalize(raw)
			if seen[word] || group.tier == 1 && genericTag(word) {
				continue
			}
			seen[word] = true
			if !phraseMatch(text, word) {
				continue
			}
			if group.tier > result.tier {
				result.tier = group.tier
			}
			if n := utf8.RuneCountInString(word); n > result.longest {
				result.longest = n
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
	if err := checkContext(ctx); err != nil {
		return Context{}, err
	}
	if r == nil {
		return Context{}, errors.New("知识Reader不能为空")
	}
	if err := ValidateScope(query.Scope); err != nil {
		return Context{}, err
	}
	if !utf8.ValidString(query.Text) {
		return Context{}, errors.New("知识查询不是UTF-8")
	}
	if len(query.Text) > 4<<20 {
		return Context{}, errors.New("知识查询超过字节上限")
	}
	result := Context{Scope: query.Scope, Revision: r.revision}
	text := normalize(query.Text)
	if !hasWord(text) {
		return result, nil
	}
	hits := make([]hit, 0, 3)
	for _, e := range r.entries {
		if err := checkContext(ctx); err != nil {
			return Context{}, err
		}
		if !inScope(e.item, query.Scope) {
			continue
		}
		score, err := scoreEntry(ctx, text, e)
		if err != nil {
			return Context{}, err
		}
		if score.tier > 0 {
			hits = append(hits, hit{entry: e, score: score})
		}
	}
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
	for i := 0; i < len(hits) && i < 3; i++ {
		result.Items = append(result.Items, cloneItem(hits[i].entry.item))
	}
	if err := checkContext(ctx); err != nil {
		return Context{}, err
	}
	return result, nil
}
