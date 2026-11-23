// Package conversation 按通道语义派生会话键。
//
// 原则（见 会话状态升级清单-20261123）：
//   - 私信按 peer（含接收账号），天然 1:1；
//   - 评论按 thread root（同一 oid 内），不跨视频、不按用户全局合并；
//   - 直播不建持久会话；需要短窗口时由调用方按 TTL 处理。
package conversation

import (
	"encoding/json"
	"strings"

	"RaiseMyFish/internal/event"
)

// Key 返回事件的会话键；返回空串表示该事件不参与持久会话。
//
// 只使用可信结构化字段（Session/ReplyTo/Meta.root），不从正文猜话题。
func Key(ev event.Event) string {
	switch ev.Channel {
	case event.ChDM:
		if actor, peer, ok := event.SplitDMSession(ev.Session); ok {
			return "dm:" + actor + ":" + peer
		}
		if peer, ok := event.DMPeer(ev); ok {
			return "dm:legacy:" + peer
		}
		return ""
	case event.ChComment:
		oid := strings.TrimSpace(ev.Session)
		if oid == "" {
			return ""
		}
		root := commentRoot(ev)
		if root == "" {
			return ""
		}
		return "comment:" + oid + ":" + root
	default:
		return ""
	}
}

// commentRoot 取根评论 rpid：楼中楼用 Meta.root，顶层评论用自己的 rpid。
func commentRoot(ev event.Event) string {
	if len(ev.Meta) > 0 {
		var meta struct {
			Root string `json:"root"`
		}
		if json.Unmarshal(ev.Meta, &meta) == nil {
			if root := strings.TrimSpace(meta.Root); root != "" {
				return root
			}
		}
	}
	return strings.TrimSpace(ev.ReplyTo)
}

// Short 返回可安全写入日志的短标识：只保留通道与哈希，不含 UID/正文。
func Short(key string) string {
	if key == "" {
		return ""
	}
	channel, _, ok := strings.Cut(key, ":")
	if !ok {
		channel = "unknown"
	}
	sum := fnv32(key)
	return channel + ":" + itoaHex(sum)
}

func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func itoaHex(v uint32) string {
	const digits = "0123456789abcdef"
	var buf [8]byte
	for i := 7; i >= 0; i-- {
		buf[i] = digits[v&0xf]
		v >>= 4
	}
	return string(buf[:])
}
