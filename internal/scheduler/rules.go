package scheduler

import (
	"crypto/sha256"
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

// mentionMiss 只对 live 生效：开启后未 @ 自己的弹幕在准入前跳过，不进入 LLM。
func mentionMiss(e event.Event, p ChannelPolicy) bool {
	return e.Channel == event.ChLive && p.ReplyOnlyMention && !containsMention(e.Text, p.SelfNames)
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
