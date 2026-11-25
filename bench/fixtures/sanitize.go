// Package main 提供压测样本的脱敏与生成工具。
//
// 约束：只处理本地 JSONL，不连接生产库；UID 一律加盐哈希，正文按 rune 截断，
// 输出可直接交给 cmd/fishload 回放。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
)

// Record 是 JSONL 的稳定输入/输出格式，字段名与事件契约一一对应。
type Record struct {
	ID             string          `json:"id"`
	Channel        event.Channel   `json:"channel"`
	Dir            event.Dir       `json:"dir"`
	Session        string          `json:"session"`
	FromID         string          `json:"from_id,omitempty"`
	FromName       string          `json:"from_name,omitempty"`
	Text           string          `json:"text"`
	Private        bool            `json:"private,omitempty"`
	ReplyTo        string          `json:"reply_to,omitempty"`
	At             time.Time       `json:"at"`
	Meta           json.RawMessage `json:"meta,omitempty"`
	CollectMode    string          `json:"collect_mode,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
}

// Event 转成事件总线类型；状态固定为 pending（入向事件初态）。
func (r Record) Event() event.Event {
	return event.Event{
		ID: r.ID, Channel: r.Channel, Dir: r.Dir, Session: r.Session,
		FromID: r.FromID, FromName: r.FromName, Text: r.Text, Private: r.Private,
		ReplyTo: r.ReplyTo, At: r.At, Status: event.StatusPending, Meta: r.Meta,
		CollectMode: r.CollectMode, ConversationID: r.ConversationID,
	}
}

// RecordFromEvent 把事件投影成可序列化记录。
func RecordFromEvent(e event.Event) Record {
	return Record{
		ID: e.ID, Channel: e.Channel, Dir: e.Dir, Session: e.Session,
		FromID: e.FromID, FromName: e.FromName, Text: e.Text, Private: e.Private,
		ReplyTo: e.ReplyTo, At: e.At, Meta: e.Meta, CollectMode: e.CollectMode,
		ConversationID: e.ConversationID,
	}
}

// SanitizeOptions 控制脱敏强度。
type SanitizeOptions struct {
	Salt       string
	MaxRunes   int
	HashPrefix string
}

// DefaultSanitizeOptions 是压测默认：盐由调用方传入，正文最多 80 rune。
func DefaultSanitizeOptions() SanitizeOptions {
	return SanitizeOptions{Salt: "raisemyfish-bench", MaxRunes: 80, HashPrefix: "u"}
}

// SanitizeEvent 返回脱敏副本：UID 哈希、正文截断、Meta 中的身份字段替换。
//
// 事件 ID 与 Session 必须同步改写，否则 store.ValidateEvent 会拒绝。
func SanitizeEvent(e event.Event, opts SanitizeOptions) event.Event {
	if opts.MaxRunes <= 0 {
		opts.MaxRunes = 80
	}
	if strings.TrimSpace(opts.Salt) == "" {
		opts.Salt = "raisemyfish-bench"
	}
	if opts.HashPrefix == "" {
		opts.HashPrefix = "u"
	}
	out := e
	out.Text = truncateRunes(e.Text, opts.MaxRunes)
	out.FromName = truncateRunes(e.FromName, 24)
	out.FromID = hashUID(e.FromID, opts)
	out.Session = sanitizeSession(e.Session, opts)
	out.ID = sanitizeID(e.ID, out.Session, opts)
	out.ReplyTo = sanitizeReplyTo(e.ReplyTo, opts)
	out.Meta = sanitizeMeta(e.Meta, opts)
	if out.ConversationID != "" {
		out.ConversationID = sanitizeConversation(out.ConversationID, opts)
	}
	return out
}

func hashUID(uid string, opts SanitizeOptions) string {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(opts.Salt + "\x00" + uid))
	return opts.HashPrefix + hex.EncodeToString(sum[:])[:12]
}

func sanitizeSession(session string, opts SanitizeOptions) string {
	parts := strings.Split(session, ":")
	if len(parts) == 2 {
		return hashUID(parts[0], opts) + ":" + hashUID(parts[1], opts)
	}
	return hashUID(session, opts)
}

func sanitizeID(id, session string, opts SanitizeOptions) string {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 {
		return hashUID(id, opts)
	}
	return parts[0] + ":" + session + ":" + parts[2]
}

func sanitizeReplyTo(replyTo string, opts SanitizeOptions) string {
	if strings.TrimSpace(replyTo) == "" {
		return ""
	}
	return hashUID(replyTo, opts)
}

func sanitizeConversation(id string, opts SanitizeOptions) string {
	if strings.Contains(id, ":") {
		return sanitizeSession(id, opts)
	}
	return hashUID(id, opts)
}

// sanitizeMeta 替换已知身份键，并删除图片 URL 等可能指向真实内容的外链。
func sanitizeMeta(raw json.RawMessage, opts SanitizeOptions) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return raw
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return raw
	}
	for _, key := range []string{"recv_actor", "uid", "mid", "from_id", "reply_mid"} {
		if value, ok := meta[key]; ok {
			if text, ok := value.(string); ok {
				meta[key] = hashUID(text, opts)
			}
		}
	}
	delete(meta, "pics")
	data, err := json.Marshal(meta)
	if err != nil {
		return raw
	}
	return data
}

func truncateRunes(text string, max int) string {
	if max <= 0 || utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max])
}

// validateRecord 在写出前做一次契约校验，避免生成不可回放的样本。
func validateRecord(r Record) error {
	if r.Channel != event.ChLive && r.Channel != event.ChComment && r.Channel != event.ChDM {
		return fmt.Errorf("通道非法：%s", r.Channel)
	}
	if r.Dir != event.DirIn {
		return fmt.Errorf("样本只接受入向事件：%s", r.Dir)
	}
	if r.At.IsZero() {
		return fmt.Errorf("缺少 at")
	}
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("正文为空")
	}
	if r.Channel == event.ChDM && !r.Private {
		return fmt.Errorf("DM 样本必须 private=true")
	}
	if r.Channel != event.ChDM && r.Private {
		return fmt.Errorf("公开通道不能 private=true")
	}
	return nil
}
