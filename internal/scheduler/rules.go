package scheduler

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

func aclAllowed(e event.Event, p ChannelPolicy) bool {
	if e.Dir != event.DirIn || memory.ValidateScope(memory.Scope{Channel: e.Channel, Session: e.Session, Private: e.Private}) != nil {
		return false
	}
	return (!e.Private && p.Public) || (e.Private && p.Private)
}
func inList(value string, list []string) bool {
	for _, v := range list {
		if v != "" && v == value {
			return true
		}
	}
	return false
}

// containsMention 匹配 @名字：兼容全角＠、@ 后空白，名字忽略 ASCII 大小写。
// 不做后缀边界判断，避免「@大肥鱼在吗」这类连写被漏掉。
func containsMention(text string, names []string) bool {
	for i, r := range text {
		if r != '@' && r != '＠' {
			continue
		}
		rest := strings.TrimLeftFunc(text[i+utf8.RuneLen(r):], unicode.IsSpace)
		if rest == "" {
			continue
		}
		lower := strings.ToLower(rest)
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name != "" && strings.HasPrefix(lower, strings.ToLower(name)) {
				return true
			}
		}
	}
	return false
}

// replyMeta 是采集器写入的结构化回复目标；损坏的 Meta 按无目标处理。
type replyMeta struct {
	ReplyToID   string `json:"reply_to_id"`
	ReplyToName string `json:"reply_to_name"`
}

func structuredReply(e event.Event) (replyMeta, bool) {
	if len(e.Meta) == 0 {
		return replyMeta{}, false
	}
	var meta replyMeta
	if json.Unmarshal(e.Meta, &meta) != nil {
		return replyMeta{}, false
	}
	return meta, true
}

// structuredMention 匹配 B站「回复/@」弹幕：正文可能没有 @，目标在 Meta 的结构化字段里。
// UID 优先（不受改名影响）；名字只做完整名忽略大小写比较，避免子串误命中。
func structuredMention(e event.Event, p ChannelPolicy) bool {
	meta, ok := structuredReply(e)
	if !ok {
		return false
	}
	if meta.ReplyToID != "" && inList(meta.ReplyToID, p.SelfIDs) {
		return true
	}
	name := strings.TrimSpace(meta.ReplyToName)
	if name == "" {
		return false
	}
	for _, self := range p.SelfNames {
		self = strings.TrimSpace(self)
		if self != "" && strings.EqualFold(name, self) {
			return true
		}
	}
	return false
}

// MentionBackTarget 返回 live 回复要 @ 的昵称。
// 生效条件：live、开启 ReplyMentionBack，且事件明确指向鱼——
// 结构化回复目标是自己，或正文直接 @ 了鱼。
// 结构化回复的 reply_to_name 是「被回复的人」；别人回复鱼时它就是鱼自己，@ 自己没意义，
// 此时改用发弹幕的人（FromName）；正文 @ 鱼的普通弹幕同样 @ 发送者。
// 都空或含控制字符返回 false。
func MentionBackTarget(e event.Event, p ChannelPolicy) (string, bool) {
	if e.Channel != event.ChLive || !p.ReplyMentionBack {
		return "", false
	}
	structured := structuredMention(e, p)
	if !structured && !containsMention(e.Text, p.SelfNames) {
		return "", false
	}
	name := ""
	if structured {
		meta, _ := structuredReply(e)
		name = strings.TrimSpace(meta.ReplyToName)
		if name != "" && isSelfName(name, p.SelfNames) {
			name = ""
		}
	}
	if name == "" {
		name = strings.TrimSpace(e.FromName)
	}
	if name == "" || !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", false
	}
	return name, true
}

// isSelfName 判断昵称是否是鱼自己（忽略大小写与首尾空白）。
func isSelfName(name string, selfNames []string) bool {
	target := strings.ToLower(strings.TrimSpace(name))
	if target == "" {
		return false
	}
	for _, self := range selfNames {
		if strings.ToLower(strings.TrimSpace(self)) == target {
			return true
		}
	}
	return false
}

// mentionOnlySkip 只对 live 生效：未 @ 自己（且不是结构化回复自己）就跳过。
func mentionOnlySkip(e event.Event, p ChannelPolicy) bool {
	return e.Channel == event.ChLive && !containsMention(e.Text, p.SelfNames) && !structuredMention(e, p)
}

// mentionMiss 是旧配置 reply_only_mention 的判定入口。
func mentionMiss(e event.Event, p ChannelPolicy) bool {
	return p.ReplyOnlyMention && mentionOnlySkip(e, p)
}

// policyRouting 把新 routing_mode 与旧 reply_only_mention 统一成一个模式。
func policyRouting(p ChannelPolicy) string {
	switch p.RoutingMode {
	case RoutingMentionOnly, RoutingFilter, RoutingAll:
		return p.RoutingMode
	}
	if p.ReplyOnlyMention {
		return RoutingMentionOnly
	}
	return RoutingAll
}

// routingSkip 决定 live 弹幕是否在准入前丢弃（唯一合法的 queued→skipped 阶段）。
func routingSkip(e event.Event, p ChannelPolicy) bool {
	if e.Channel != event.ChLive {
		return false
	}
	switch policyRouting(p) {
	case RoutingMentionOnly:
		return mentionOnlySkip(e, p)
	case RoutingFilter:
		return !filterRelevant(e, p)
	default:
		return false
	}
}

// 规则筛选器的互动线索：提问、请求、以及指向上下文的指代。
// 只做确定性近似；调度器是单线程，不能在这里调小模型或读最近事件。
var (
	liveQuestionCues = []string{
		"?", "？", "吗", "什么", "怎么", "怎样", "为什么", "为啥", "哪", "谁", "多少", "几点",
		"是不是", "有没有", "能不能", "可不可以", "会不会", "对不对", "好不好", "行不行", "要不要",
		"在吗", "在不在", "有人吗",
	}
	liveRequestCues = []string{
		"帮我", "帮忙", "来一个", "来一首", "来首", "唱一首", "唱首", "唱个", "讲个", "讲讲", "说说",
		"解释一下", "查一下", "搜一下", "推荐一下", "教我", "给我讲", "你会", "你能", "陪我",
	}
	liveContextCues = []string{
		"刚才", "刚刚", "上一条", "前面说", "你说", "你说的", "你刚", "继续", "再来", "还有吗", "然后呢", "接着",
	}
)

// filterRelevant 是 routing_mode=filter 的规则筛选器：
// 直接 @/结构化回复/叫鱼名字/主人/白名单直接放行；
// 其余用提问、请求、上下文指代判断是否有互动价值；纯闲聊（复读、表情、寒暄）返回 false。
func filterRelevant(e event.Event, p ChannelPolicy) bool {
	if e.Channel != event.ChLive {
		return true
	}
	text := strings.TrimSpace(e.Text)
	if text == "" {
		return false
	}
	if containsMention(text, p.SelfNames) || structuredMention(e, p) {
		return true
	}
	if inList(e.FromID, p.OwnerIDs) || inList(text, p.Whitelist) {
		return true
	}
	if containsSelfName(text, p.SelfNames) {
		return true
	}
	return containsAnyCue(text, liveQuestionCues) ||
		containsAnyCue(text, liveRequestCues) ||
		containsAnyCue(text, liveContextCues)
}

// containsSelfName 判断正文是否直接叫了鱼的名字（不带 @）。
func containsSelfName(text string, names []string) bool {
	lower := strings.ToLower(text)
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" && strings.Contains(lower, name) {
			return true
		}
	}
	return false
}

func containsAnyCue(text string, cues []string) bool {
	lower := strings.ToLower(text)
	for _, cue := range cues {
		if cue != "" && strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

func noiseReason(e event.Event, p ChannelPolicy) string {
	if inList(e.FromID, p.SelfIDs) {
		return "self"
	}
	if inList(e.FromID, p.BotIDs) {
		return "bot"
	}
	text := strings.TrimSpace(e.Text)
	lower := strings.ToLower(text)
	for _, ad := range p.Ads {
		if strings.TrimSpace(ad) != "" && strings.Contains(lower, strings.ToLower(ad)) {
			return "advertisement"
		}
	}
	if inList(text, p.Whitelist) {
		return ""
	}
	if utf8.RuneCountInString(text) < p.MinRunes {
		return "short"
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return ""
		}
	}
	return "symbols"
}
func priority(e event.Event, p ChannelPolicy) int {
	if inList(e.FromID, p.OwnerIDs) {
		return 0
	}
	text := strings.ToLower(e.Text)
	for _, name := range p.Names {
		if strings.TrimSpace(name) != "" && strings.Contains(text, strings.ToLower(name)) {
			return 1
		}
	}
	return 2
}
func dayWindow(now time.Time, location *time.Location) (time.Time, time.Time) {
	local := now.In(location)
	from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	return from, from.AddDate(0, 0, 1)
}

type sessionKey struct {
	channel event.Channel
	session string
}

func session(e event.Event) sessionKey { return sessionKey{e.Channel, e.Session} }

type repeatKey struct {
	session sessionKey
	private bool
	from    string
	digest  [32]byte
}

func repetition(e event.Event) repeatKey {
	return repeatKey{session(e), e.Private, e.FromID, sha256.Sum256([]byte(strings.TrimSpace(e.Text)))}
}
