// Package brain 将已校验事件/人格/记忆转换为文本回复，不直接发送或访问SQL。
package brain

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Brain 是大脑执行器契约。
type Brain interface {
	Name() string
	Reply(context.Context, Task, ContextInput) (Reply, error)
}

// Task 引用可信事件；调度策略留scheduler。
type Task struct {
	ID      string
	EventID string
	Channel event.Channel
	Session string
}

// Tool 仅保留名字；P0禁止非空工具输入。
type Tool struct{ Name string }

// Action 仅保留名字；P0结果必须为空。
type Action struct{ Name string }

// ContextInput 必须显式携带相同目标Scope。
type ContextInput struct {
	Event     event.Event
	Persona   persona.Pack
	Memory    memory.Context
	Shared    memory.SharedState
	Knowledge *knowledge.Context
	Tools     []Tool
	// State 是当前状态摘要（如正在看谁的直播）；空表示不注入 prompt。
	State string
	// Visual 是画面描述（主动弹幕/看直播注入）；空则不出现 <visual> 段。
	Visual string
	// Viewer 是对方档案片段（认人用）；空则不出现 <viewer> 段（陌生人）。
	Viewer string
	// Soul 是长期设定（灵魂记忆），每行一条；空则不出现 <longterm> 段。
	Soul []string
}

// Reply 不包含原始prompt或模型凭据。
type Reply struct {
	Text    string
	Actions []Action
	Meta    json.RawMessage
}

// Options 控制单次生成，不包含全局调度或日配额。
type Options struct {
	Model                  string
	MaxPromptTokens        int
	KnowledgeBudgetPercent int
	MaxReplyTokens         int
	AttemptTimeout         time.Duration
	TotalTimeout           time.Duration
	Backoff                time.Duration
	MaxConcurrent          int
	Temperature            float64
	InputUSDPerMillion     *float64
	OutputUSDPerMillion    *float64
}

// DefaultOptions 使用保守字节预算，实际费用只依据服务端用量。
func DefaultOptions() Options {
	return Options{MaxPromptTokens: 8192, KnowledgeBudgetPercent: 10, MaxReplyTokens: 128, AttemptTimeout: 10 * time.Second, TotalTimeout: 40 * time.Second, Backoff: 250 * time.Millisecond, MaxConcurrent: 4, Temperature: .7}
}

// Kind 是日志可安全输出的错误类型。
type Kind string

const (
	KindInput    Kind = "input"
	KindBudget   Kind = "budget"
	KindProvider Kind = "provider"
	KindTimeout  Kind = "timeout"
	KindCanceled Kind = "canceled"
	KindEmpty    Kind = "empty"
	KindUsage    Kind = "usage"
)

// Error 保留原因链但不展示底层敏感详情；Usage失败不可自动重试生成。
type Error struct {
	Kind      Kind
	Retryable bool
	Cause     error
}

func (e *Error) Error() string { return fmt.Sprintf("生成回复失败（类别%s）", e.Kind) }
func (e *Error) Unwrap() error { return e.Cause }
