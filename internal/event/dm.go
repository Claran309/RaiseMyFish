package event

// 私信事件的归属辅助：会话键与接收账号。
//
// 同一对方 UID 可能同时给多个账号发私信，仅用对方 UID 当 Session 会让两个
// 接收账号的私密会话在记忆里混成一个。采集器把事件落成
//   Session = "{接收账号MID}:{对方UID}"，Meta 里另存 recv_actor；
// 旧数据（Session 只有对方 UID）仍能被读取，但缺少接收账号时调用方必须
// 明确拒绝，不能猜测成当前活跃账号。

import (
	"encoding/json"
	"strings"
)

// MetaRecvActor 是私信事件 Meta 中记录接收账号（B站 MID）的键。
const MetaRecvActor = "recv_actor"

// DMSession 拼接私信会话键。
func DMSession(recvActor, peer string) string {
	return strings.TrimSpace(recvActor) + ":" + strings.TrimSpace(peer)
}

// SplitDMSession 拆分 "{接收账号}:{对方UID}"；不是合法复合键时返回 false。
func SplitDMSession(session string) (recvActor, peer string, ok bool) {
	parts := strings.Split(strings.TrimSpace(session), ":")
	if len(parts) != 2 {
		return "", "", false
	}
	recvActor, peer = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if !digits(recvActor) || !digits(peer) {
		return "", "", false
	}
	return recvActor, peer, true
}

// DMRecvActor 返回私信事件持久化的接收账号。
//
// Session 的复合键与 Meta 的 recv_actor 都写了接收账号；两者不一致说明事件
// 归属已损坏，返回 false 让调用方冻结这条任务，而不是任选一个继续发送。
func DMRecvActor(e Event) (string, bool) {
	sessionActor, _, hasSession := SplitDMSession(e.Session)
	metaActor, hasMeta := dmMetaRecvActor(e)
	switch {
	case hasMeta && hasSession:
		return metaActor, metaActor == sessionActor
	case hasMeta:
		return metaActor, true
	case hasSession:
		return sessionActor, true
	}
	return "", false
}

// DMRecvActorConflict 报告事件是否携带互相矛盾的接收账号。
//
// 只有 Session 复合键与 Meta.recv_actor 都给出合法账号且不一致时才算
// 明确冲突；两者都缺失属于旧数据，单方缺失则按另一方归属，均不算冲突。
func DMRecvActorConflict(e Event) bool {
	sessionActor, _, hasSession := SplitDMSession(e.Session)
	metaActor, hasMeta := dmMetaRecvActor(e)
	return hasSession && hasMeta && sessionActor != metaActor
}

// dmMetaRecvActor 读取 Meta 里的合法接收账号；缺失或非法都视为没有。
func dmMetaRecvActor(e Event) (string, bool) {
	if len(e.Meta) == 0 {
		return "", false
	}
	var meta struct {
		RecvActor string `json:"recv_actor"`
	}
	if json.Unmarshal(e.Meta, &meta) != nil {
		return "", false
	}
	actor := strings.TrimSpace(meta.RecvActor)
	if !digits(actor) {
		return "", false
	}
	return actor, true
}

// DMPeer 返回私信事件的对话对方 UID（收信人）。
//
// 兼容三种形态：新复合 Session、旧裸 Session、只有 FromID 的旧事件。
func DMPeer(e Event) (string, bool) {
	if _, peer, ok := SplitDMSession(e.Session); ok {
		return peer, true
	}
	if peer := strings.TrimSpace(e.Session); digits(peer) {
		return peer, true
	}
	if peer := strings.TrimSpace(e.FromID); digits(peer) {
		return peer, true
	}
	return "", false
}

func digits(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
