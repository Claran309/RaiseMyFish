package memory

// 本文件实现 #93 的「重复度触发」纯函数：
// 把别名表 / 观众档案名 / @ 目标汇总成实体，在公开事件窗口里统计提及次数，
// 达标后生成一条固定称呼事实。全部纯函数，不读库、不写库、不依赖时钟。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
)

const (
	// RepeatWindowDays 是重复度统计窗口：7 天。
	RepeatWindowDays = 7
	// repeatMaxRepresentative 是一条事实最多保留几条代表事件，避免 evidence 膨胀。
	repeatMaxRepresentative = 5
	// repeatMinAliasRunes 是参与统计的最短称呼：单字称呼误报太多。
	repeatMinAliasRunes = 2
)

// RepeatAlias 是别名表里一条已确认的「称呼 -> 实体」映射。
type RepeatAlias struct {
	Alias string
	UID   string
	Name  string
}

// RepeatName 是一个已知昵称（观众档案或 @ 目标）。
type RepeatName struct {
	UID  string
	Name string
}

// RepeatEntity 是参与重复度统计的实体。
//
// Alias 是弹幕里实际出现的称呼，Target 是它指向的人；@ 目标与档案名
// 没有额外信息时 Alias 与 Target 相同。
type RepeatEntity struct {
	Alias  string
	Target string
	UID    string
	Source string // alias / profile / reply
}

// RepeatCandidate 是一个达到阈值的实体及其证据。
type RepeatCandidate struct {
	Entity   RepeatEntity
	Count    int
	EventIDs []string
	FirstAt  time.Time
	LastAt   time.Time
}

// repeatMeta 只解析重复度需要的元数据，避免把整条 Meta 带进统计。
type repeatMeta struct {
	ReplyToName string `json:"reply_to_name"`
}

// RepeatEntities 合并三类实体：别名表优先，其次观众档案名，最后 @ 目标。
//
// 别名表是人工确认的映射，优先级最高；档案名/@ 目标如果正好是某个别名的
// 目标或别名本身，就不再单独建档，避免同一个人被统计两次。
func RepeatEntities(aliases []RepeatAlias, profileNames, replyNames []RepeatName) []RepeatEntity {
	seen := make(map[string]bool, len(aliases)+len(profileNames)+len(replyNames))
	targets := make(map[string]bool, len(aliases))
	out := make([]RepeatEntity, 0, len(aliases)+len(profileNames)+len(replyNames))

	for _, a := range aliases {
		alias := strings.TrimSpace(a.Alias)
		if !usableMention(alias) {
			continue
		}
		key := normalizeMention(alias)
		if seen[key] {
			continue
		}
		target := strings.TrimSpace(a.Name)
		uid := strings.TrimSpace(a.UID)
		if target == "" {
			target = uid
		}
		if target == "" {
			target = alias
		}
		seen[key] = true
		targets[normalizeMention(target)] = true
		out = append(out, RepeatEntity{Alias: alias, Target: target, UID: uid, Source: "alias"})
	}

	appendNames := func(names []RepeatName, source string) {
		for _, n := range names {
			name := strings.TrimSpace(n.Name)
			if !usableMention(name) {
				continue
			}
			key := normalizeMention(name)
			if seen[key] || targets[key] {
				continue
			}
			seen[key] = true
			out = append(out, RepeatEntity{Alias: name, Target: name, UID: strings.TrimSpace(n.UID), Source: source})
		}
	}
	appendNames(profileNames, "profile")
	appendNames(replyNames, "reply")
	return out
}

// FilterRepeatEntities 去掉鱼自己的名字：弹幕里叫鱼不算观众称呼。
func FilterRepeatEntities(entities []RepeatEntity, ignoreNames []string) []RepeatEntity {
	ignore := make(map[string]bool, len(ignoreNames))
	for _, name := range ignoreNames {
		if key := normalizeMention(name); key != "" {
			ignore[key] = true
		}
	}
	if len(ignore) == 0 {
		return entities
	}
	out := make([]RepeatEntity, 0, len(entities))
	for _, e := range entities {
		if ignore[normalizeMention(e.Alias)] || ignore[normalizeMention(e.Target)] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// RepeatReplyNames 从事件 Meta 里抽取 @ 目标昵称（只取公开入向）。
func RepeatReplyNames(events []Event) []RepeatName {
	seen := make(map[string]bool, len(events))
	out := make([]RepeatName, 0, len(events))
	for _, ev := range events {
		if !repeatEventUsable(ev, time.Time{}) {
			continue
		}
		name := replyNameOf(ev)
		if !usableMention(name) {
			continue
		}
		key := normalizeMention(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, RepeatName{Name: name})
	}
	return out
}

// CountRepeatMentions 在窗口内统计每个实体的公开提及次数，只返回达到阈值的候选。
//
// 一条事件对同一实体最多计一次；Alias 按正文匹配，Target 按 reply_to_name 匹配。
// 代表事件最多 5 条（最新优先），作为事实证据；没有任何事件 ID 的候选直接丢弃。
func CountRepeatMentions(entities []RepeatEntity, events []Event, since time.Time, threshold int) []RepeatCandidate {
	if threshold < 2 || len(entities) == 0 || len(events) == 0 {
		return nil
	}
	type acc struct {
		count    int
		mentions []repeatMention
		first    time.Time
		last     time.Time
		seen     map[string]bool
	}
	accs := make([]acc, len(entities))

	for _, ev := range events {
		if !repeatEventUsable(ev, since) {
			continue
		}
		replyName := replyNameOf(ev)
		if strings.TrimSpace(ev.Text) == "" && replyName == "" {
			continue
		}
		for i := range entities {
			if !entityMentioned(entities[i], ev.Text, replyName) {
				continue
			}
			a := &accs[i]
			if a.seen == nil {
				a.seen = make(map[string]bool, 4)
			}
			if a.seen[ev.ID] {
				continue
			}
			a.seen[ev.ID] = true
			a.count++
			a.mentions = append(a.mentions, repeatMention{ID: ev.ID, At: ev.At})
			if a.first.IsZero() || ev.At.Before(a.first) {
				a.first = ev.At
			}
			if ev.At.After(a.last) {
				a.last = ev.At
			}
		}
	}

	out := make([]RepeatCandidate, 0, len(entities))
	for i := range entities {
		a := &accs[i]
		if a.count < threshold {
			continue
		}
		sort.Slice(a.mentions, func(x, y int) bool {
			if !a.mentions[x].At.Equal(a.mentions[y].At) {
				return a.mentions[x].At.After(a.mentions[y].At)
			}
			return a.mentions[x].ID > a.mentions[y].ID
		})
		ids := make([]string, 0, repeatMaxRepresentative)
		for _, m := range a.mentions {
			if m.ID == "" {
				continue
			}
			ids = append(ids, m.ID)
			if len(ids) == repeatMaxRepresentative {
				break
			}
		}
		if len(ids) == 0 {
			continue
		}
		out = append(out, RepeatCandidate{
			Entity: entities[i], Count: a.count, EventIDs: ids,
			FirstAt: a.first, LastAt: a.last,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Entity.Alias != out[j].Entity.Alias {
			return out[i].Entity.Alias < out[j].Entity.Alias
		}
		return out[i].Entity.Target < out[j].Entity.Target
	})
	return out
}

// repeatMention 是一条命中的代表事件。
type repeatMention struct {
	ID string
	At time.Time
}

// DedupeRepeatCandidates 按实体去重，保留输入里靠前的候选。
//
// 输入应已按 Count 降序排列；同一实体有多个别名时只保留提及最多的那个，
// 保证「同实体 24 小时最多触发一次」。
func DedupeRepeatCandidates(in []RepeatCandidate) []RepeatCandidate {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]RepeatCandidate, 0, len(in))
	for _, c := range in {
		key := RepeatEntityKey(c.Entity)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// RepeatEntityKey 是实体身份的稳定标识：优先 UID，没有 UID 时用目标名。
//
// 同一目标有多个别名时 key 相同，用于「同实体 24 小时最多触发一次」去重。
func RepeatEntityKey(e RepeatEntity) string {
	if uid := strings.TrimSpace(e.UID); uid != "" {
		return "uid:" + uid
	}
	target := normalizeMention(e.Target)
	if target == "" {
		target = normalizeMention(e.Alias)
	}
	if target == "" {
		return ""
	}
	return "name:" + target
}

// RepeatFactKey 是固定称呼事实的稳定标识：称呼 + 指向。
//
// 刻意不用 UID：改称呼后事实标题也必须跟着改，复用旧行会留下
// 「标题还是旧称呼、摘要已经换成新称呼」的脏数据。
func RepeatFactKey(e RepeatEntity) string {
	alias := normalizeMention(e.Alias)
	if alias == "" {
		return ""
	}
	target := normalizeMention(e.Target)
	if target == "" || target == alias {
		return "name:" + alias
	}
	return "alias:" + alias + ">" + target
}

// RepeatFactTitle 生成固定称呼事实的标题。
//
// 别名指向别人时用「称呼：A = B」；档案名/@ 目标没有额外指向时只写称呼。
func RepeatFactTitle(alias, target string) string {
	alias = strings.TrimSpace(alias)
	target = strings.TrimSpace(target)
	if alias == "" {
		return ""
	}
	if target == "" || strings.EqualFold(target, alias) {
		return truncateRunes("称呼："+alias, maxTitleRunes)
	}
	return truncateRunes("称呼："+alias+" = "+target, maxTitleRunes)
}

// RepeatFactSummary 生成事实摘要，写法与派发单一致：
// 弹幕里「奇迹鱼」指的是 X奇迹与你X（7 天内提及 14 次）。
func RepeatFactSummary(alias, target string, count, days int) string {
	alias = strings.TrimSpace(alias)
	target = strings.TrimSpace(target)
	if alias == "" || count < 1 {
		return ""
	}
	if days < 1 {
		days = RepeatWindowDays
	}
	if target == "" || strings.EqualFold(target, alias) {
		return truncateRunes(fmt.Sprintf("弹幕里多次提及「%s」（%d 天内共 %d 次）", alias, days, count), maxSummaryRunes)
	}
	return truncateRunes(fmt.Sprintf("弹幕里「%s」指的是 %s（%d 天内提及 %d 次）", alias, target, days, count), maxSummaryRunes)
}

// repeatCorrectionMarkers 是主人纠正称呼时的明确用语。
//
// 只收「命名被否定」的强信号；普通否定词（不是/不对）单独出现太容易误判，不列入。
var repeatCorrectionMarkers = []string{
	"记错了", "搞错了", "弄错了", "说错了", "讲错了", "叫错了",
	"别乱说", "没这回事", "不是这么叫", "不叫",
}

// CorrectedRepeatAliases 找出被主人公开纠正的实体，返回实体 key 集合。
//
// 只认主人 UID 的公开入向发言：文本同时含纠正用语和被纠正的称呼才成立；
// 疑问句（「奇迹鱼不是X吗？」）不算纠正。
func CorrectedRepeatAliases(events []Event, entities []RepeatEntity, ownerUID string) map[string]bool {
	ownerUID = strings.TrimSpace(ownerUID)
	if ownerUID == "" || len(entities) == 0 {
		return nil
	}
	corrected := make(map[string]bool, 2)
	for _, ev := range events {
		if ev.Dir != event.DirIn || ev.Private || ev.Channel == event.ChDM || !ev.Channel.Valid() {
			continue
		}
		if strings.TrimSpace(ev.FromID) != ownerUID {
			continue
		}
		text := strings.TrimSpace(ev.Text)
		if text == "" || isQuestionText(text) || !containsAny(text, repeatCorrectionMarkers) {
			continue
		}
		for _, e := range entities {
			if uid := strings.TrimSpace(e.UID); uid != "" && uid == ownerUID {
				continue
			}
			if e.Alias != "" && mentionsAlias(text, e.Alias) {
				if key := RepeatEntityKey(e); key != "" {
					corrected[key] = true
				}
			}
		}
	}
	if len(corrected) == 0 {
		return nil
	}
	return corrected
}

// isQuestionText 判断一句话是不是疑问句：纠正必须陈述，不能靠问句降权。
func isQuestionText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if strings.HasSuffix(text, "?") || strings.HasSuffix(text, "？") {
		return true
	}
	return containsAny(text, []string{"吗", "呢", "是不是", "对不对"})
}

// replyNameOf 解析事件 Meta 里的 reply_to_name；解析失败按没有 @ 目标处理。
func replyNameOf(ev Event) string {
	if len(ev.Meta) == 0 {
		return ""
	}
	var meta repeatMeta
	if err := json.Unmarshal(ev.Meta, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.ReplyToName)
}

// repeatEventUsable 判断事件是否进入重复度统计：公开、入向、非 DM、有事件 ID。
func repeatEventUsable(ev Event, since time.Time) bool {
	if strings.TrimSpace(ev.ID) == "" || ev.Dir != event.DirIn || ev.Private {
		return false
	}
	if !ev.Channel.Valid() || ev.Channel == event.ChDM {
		return false
	}
	if !since.IsZero() && ev.At.Before(since) {
		return false
	}
	return true
}

// entityMentioned 判断一条事件是否提到实体：正文称呼或 reply_to_name 指向。
func entityMentioned(e RepeatEntity, text, replyName string) bool {
	if e.Alias != "" && mentionsAlias(text, e.Alias) {
		return true
	}
	if e.Target != "" && replyName != "" && normalizeMention(replyName) == normalizeMention(e.Target) {
		return true
	}
	return false
}

// mentionsAlias 判断正文是否提到称呼；ASCII 称呼按词边界匹配，避免 B 命中 B站。
func mentionsAlias(text, alias string) bool {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return false
	}
	if isASCIIWord(alias) {
		lowerText, lowerName := strings.ToLower(text), strings.ToLower(alias)
		for start := 0; start+len(lowerName) <= len(lowerText); {
			pos := strings.Index(lowerText[start:], lowerName)
			if pos < 0 {
				return false
			}
			pos += start
			end := pos + len(lowerName)
			if (pos == 0 || !isWordByte(lowerText[pos-1])) && (end == len(lowerText) || !isWordByte(lowerText[end])) {
				return true
			}
			start = end
		}
		return false
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(alias))
}

// normalizeMention 归一化称呼：转小写、去首尾空白、合并中间空白。
func normalizeMention(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// usableMention 判断称呼是否适合统计：至少 2 个字符且含字母，纯数字/纯符号不统计。
func usableMention(s string) bool {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) < repeatMinAliasRunes {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// isASCIIWord 判断称呼是否由 ASCII 字母/数字/下划线/连字符组成。
func isASCIIWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isWordByte(s[i]) {
			return false
		}
	}
	return true
}

// isWordByte 判断字节是否属于 ASCII 单词字符。
func isWordByte(c byte) bool {
	return c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
