package live

import (
	"RaiseMyFish/internal/event"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var decimal = regexp.MustCompile(`^[0-9]+$`)
var messageID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func canonicalID(id string) (string, bool) {
	if !decimal.MatchString(id) {
		return "", false
	}
	id = strings.TrimLeft(id, "0")
	return id, id != ""
}

// wireFrame是独立子进程协议，不接收任意平台Meta或DSH对象。
type wireFrame struct {
	Protocol    int    `json:"protocol"`
	Type        string `json:"type"`
	RoomID      string `json:"room_id,omitempty"`
	SelfID      string `json:"self_id,omitempty"`
	MsgID       string `json:"msg_id,omitempty"`
	FromID      string `json:"from_id,omitempty"`
	FromName    string `json:"from_name,omitempty"`
	Text        string `json:"text,omitempty"`
	ReplyToID   string `json:"reply_to_id,omitempty"`
	ReplyToName string `json:"reply_to_name,omitempty"`
	At          string `json:"at,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Fatal       bool   `json:"fatal,omitempty"`
	Retryable   bool   `json:"retryable,omitempty"`
}

func normalize(f wireFrame, room, self string) (event.Event, bool, error) {
	invalid := func() (event.Event, bool, error) { return event.Event{}, false, &Error{Kind: KindProtocol} }
	from, valid := canonicalID(f.FromID)
	if f.Protocol != 1 || f.Type != "event" || f.RoomID != room || !valid || !messageID.MatchString(f.MsgID) || strings.TrimSpace(f.Text) == "" || !utf8.ValidString(f.Text) || strings.ContainsRune(f.Text, 0) || strings.ContainsRune(f.FromName, 0) || utf8.RuneCountInString(f.FromName) > 128 {
		return invalid()
	}
	replyTo := ""
	if f.ReplyToID != "" {
		var ok bool
		if replyTo, ok = canonicalID(f.ReplyToID); !ok {
			return invalid()
		}
	}
	if f.ReplyToName != "" && (!utf8.ValidString(f.ReplyToName) || utf8.RuneCountInString(f.ReplyToName) > 128 || strings.ContainsFunc(f.ReplyToName, func(r rune) bool { return r < 0x20 || r == 0x7f })) {
		return invalid()
	}
	replyName := strings.TrimSpace(f.ReplyToName)
	at, err := time.Parse(time.RFC3339Nano, f.At)
	if err != nil || at.IsZero() {
		return invalid()
	}
	if from == self {
		return event.Event{}, false, nil
	}
	text := f.Text
	runes := []rune(text)
	truncated := len(runes) > 4000
	if truncated {
		text = string(runes[:4000])
	}
	meta, _ := json.Marshal(struct {
		Source      string `json:"source"`
		Truncated   bool   `json:"truncated,omitempty"`
		Original    int    `json:"original_runes,omitempty"`
		ReplyToID   string `json:"reply_to_id,omitempty"`
		ReplyToName string `json:"reply_to_name,omitempty"`
	}{"bili-whale-ws", truncated, len(runes), replyTo, replyName})
	return event.Event{ID: "live:" + room + ":" + f.MsgID, Channel: event.ChLive, Dir: event.DirIn, Session: room, FromID: from, FromName: f.FromName, Text: text, Private: false, At: at.UTC(), Status: event.StatusPending, Meta: meta}, true, nil
}
