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
