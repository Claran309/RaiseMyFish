// Package knowledge 加载独立知识快照并按通道和权限召回，不读取聊天记忆或调用模型。
// 契约：contracts/knowledge.md（P05-KNOWLEDGE-1）。
package knowledge

import (
	"context"

	"RaiseMyFish/internal/event"
)

// Scope 是可信事件的目标范围；私密不代表已认证主人身份。
type Scope struct {
	Channel event.Channel
	Session string
	Private bool
}

// Query 只包含范围和消息正文，不接收评测答案。
type Query struct {
	Scope Scope
	Text  string
}

// Item 是审核后的资料投影；Scenes 和 Origin 供调用方再次校验。
type Item struct {
	ID       string
	Source   string
	Text     string
	Scenes   []string
	Priority int
	Private  bool
	Origin   *Scope
}

// Context 即使零命中也保留范围与实际读取的文件版本。
type Context struct {
	Scope    Scope
	Revision string
	Items    []Item
}

// Reader 返回最多三条确定排序的候选，最终 prompt 预算由 Brain 决定。
type Reader interface {
	Recall(ctx context.Context, query Query) (Context, error)
}
