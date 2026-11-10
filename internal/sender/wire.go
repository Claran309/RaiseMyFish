package sender

// 本文件把「展示文本」规范成「实际 wire 文本 + 结构化 @ 目标」。
//
// 发送计划必须在落库前就固定这份快照：POST、核验、out 审计与防重指纹
// 都读同一份文本，不能再出现「计划存 @昵称 正文、实际发正文」的两套表示。

import (
	"RaiseMyFish/internal/event"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LiveWire 是一条直播回复的不可变发送快照。
// Text 是实际送进传输层/恢复核验的正文；ReplyMID/ReplyName 是平台结构化 @ 目标。
type LiveWire struct {
	Text      string
	ReplyMID  string
	ReplyName string
}

// LiveWireFor 从事件 Meta 与带 @ 前缀的回复正文生成规范快照。
// 没有结构化目标时原样返回正文；剥空前缀会留空时保留原文，避免发出空弹幕。
func LiveWireFor(e event.Event, text, selfID string) LiveWire {
	mid, name := replyTarget(e, text, selfID)
	if mid == "" {
		return LiveWire{Text: text}
	}
	return LiveWire{Text: stripMentionPrefix(text, name), ReplyMID: mid, ReplyName: name}
}

// ValidReplyName 允许空昵称，但拒绝未修剪、超长、含控制字符或非法UTF-8的展示名。
func ValidReplyName(name string) bool {
	if name == "" {
		return true
	}
	if !utf8.ValidString(name) || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 128 {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

type replyTargetMeta struct {
	ReplyToID   string `json:"reply_to_id"`
	ReplyToName string `json:"reply_to_name"`
}

func usableReplyName(name string) string {
	name = strings.TrimSpace(name)
	if !ValidReplyName(name) {
		return ""
	}
	return name
}

// mentionsName 判断正文是否以「@昵称」开头；后接空白或结尾才算，避免 @小鱼 命中 @小鱼干。
func mentionsName(text, name string) bool {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	for _, marker := range []string{"@", "＠"} {
		rest, ok := strings.CutPrefix(trimmed, marker)
		if !ok || !strings.HasPrefix(rest, name) {
			continue
		}
		tail := rest[len(name):]
		if tail == "" {
			return true
		}
		if r, _ := utf8.DecodeRuneInString(tail); unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// stripMentionPrefix 去掉正文开头的「@昵称」前缀。
//
// 结构化 @（reply_mid）由平台渲染高亮，正文再带 @昵称会出现双重 @；
// 因此发送前在有结构化目标时剥离文本前缀。剥空时保留原文，避免发出空弹幕。
func stripMentionPrefix(text, name string) string {
	name = usableReplyName(name)
	if name == "" || !mentionsName(text, name) {
		return text
	}
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	for _, marker := range []string{"@", "＠"} {
		if rest, ok := strings.CutPrefix(trimmed, marker); ok {
			rest = strings.TrimSpace(rest[len(name):])
			if rest != "" {
				return rest
			}
			return text
		}
	}
	return text
}

// replyTarget 从事件Meta与已带@前缀的正文反推结构化@目标。
//
// B站语义：别人回复/@鱼时 reply_to_id 是鱼自己，此时要 @ 的是发弹幕的人；
// 只有回复对象确实是其他观众时，才使用 reply_to_id/reply_to_name。
func replyTarget(e event.Event, text, selfID string) (string, string) {
	if e.Channel != event.ChLive || len(e.Meta) == 0 {
		return "", ""
	}
	var meta replyTargetMeta
	if json.Unmarshal(e.Meta, &meta) != nil {
		return "", ""
	}
	fromName, replyName := usableReplyName(e.FromName), usableReplyName(meta.ReplyToName)
	if fromName != "" && fromName != replyName && mentionsName(text, fromName) && ValidActor(e.FromID) && e.FromID != selfID {
		return e.FromID, fromName
	}
	if replyName != "" && mentionsName(text, replyName) && ValidActor(meta.ReplyToID) && meta.ReplyToID != selfID {
		return meta.ReplyToID, replyName
	}
	return "", ""
}
