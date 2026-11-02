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
// 仅在 live、开启 ReplyMentionBack 且事件是结构化 @ 自己时生效；
// 昵称优先 Meta.reply_to_name，缺失时回落到发送者 FromName；空昵称或含控制字符返回 false。
func MentionBackTarget(e event.Event, p ChannelPolicy) (string, bool) {
	if e.Channel != event.ChLive || !p.ReplyMentionBack || !structuredMention(e, p) {
		return "", false
	}
	meta, _ := structuredReply(e)
	name := strings.TrimSpace(meta.ReplyToName)
	if name == "" {
		name = strings.TrimSpace(e.FromName)
	}
	if name == "" || !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", false
	}
	return name, true
}

// mentionMiss 只对 live 生效：开启后未 @ 自己的弹幕在准入前跳过，不进入 LLM。
func mentionMiss(e event.Event, p ChannelPolicy) bool {
	return e.Channel == event.ChLive && p.ReplyOnlyMention && !containsMention(e.Text, p.SelfNames) && !structuredMention(e, p)
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
