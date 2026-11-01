package memory

// 本文件做 0.9c 的「召回装配」：把三次既有查询拼成 Brain 要的 Context / SharedState。
//
// 边界：
//   - 不写 SQL、不做过滤：隐私与范围过滤在 repository 的 SQL 里（contracts/memory.md §3），
//     本层只做参数校验、窗口控制与 DTO 二次校验。
//   - 不 import internal/store：store 已经 import memory，反向依赖会形成循环。
//     这里用窄接口 RecordSource / FactSource，store.MemoryRepo 在结构上直接满足。
//   - 查询失败一律返回 error，绝不把失败伪装成「没有记忆」（contracts/memory.md §2）。

import (
	"context"
	"errors"
	"fmt"
)

// RecordSource 是 Reader 需要的只读能力，store.MemoryRepo 天然满足。
//
// 三个方法都必须由实现方在 SQL 层做范围与隐私过滤；本包只负责组装与校验。
type RecordSource interface {
	// Recent 取同会话最近消息，升序返回。
	Recent(ctx context.Context, scope Scope, limit int) ([]Message, error)
	// CrossChannel 取其他通道的公开消息，升序返回。
	CrossChannel(ctx context.Context, scope Scope, limit int) ([]Message, error)
	// Records 取当前有效的记忆摘要，按重要度降序返回。
	Records(ctx context.Context, scope Scope, limit int) ([]Record, error)
}

// FactSource 是可选的共享事实来源。
//
// 既有 MemoryRepo 没有事实查询方法，所以这里做成可注入：没有实现就先不接，
// Shared 会返回「显式空 Facts」，而不是编一份假数据或把缺失当成失败。
type FactSource interface {
	Facts(ctx context.Context, scope Scope, limit int) ([]Fact, error)
}

// 召回窗口默认值；limit 统一夹在 [1, maxReaderLimit]。
const (
	defaultRecentLimit  = 20
	defaultCrossLimit   = 10
	defaultRecordsLimit = 10
	defaultFactsLimit   = 10
	maxReaderLimit      = 100
)

// ReaderOptions 是各段召回的窗口大小；零值用默认值。
type ReaderOptions struct {
	// RecentLimit 同会话最近消息条数，默认 20。
	RecentLimit int
	// CrossLimit 跨通道公开消息条数，默认 10。
	CrossLimit int
	// RecordsLimit 记忆摘要条数，默认 10。
	RecordsLimit int
	// FactsLimit 共享事实条数，默认 10（仅在注入了 FactSource 时生效）。
	FactsLimit int
}

// AssembledReader 是 Reader 的装配实现：三段查询 + 二次校验。
type AssembledReader struct {
	source RecordSource
	facts  FactSource
	opts   ReaderOptions
}

// 编译期断言：装配结果必须满足 Brain 依赖的 Reader 接口。
var _ Reader = (*AssembledReader)(nil)

// NewReader 构造召回装配器。
//
// facts 可以为 nil（当前没有事实来源），此时 Shared 返回空 Facts。
func NewReader(source RecordSource, facts FactSource, opts ReaderOptions) (*AssembledReader, error) {
	if source == nil {
		return nil, errors.New("记忆查询来源不能为 nil")
	}
	return &AssembledReader{source: source, facts: facts, opts: normalizeReaderOptions(opts)}, nil
}

// normalizeReaderOptions 把零值补默认、把越界夹回合法区间。
func normalizeReaderOptions(opts ReaderOptions) ReaderOptions {
	opts.RecentLimit = clampLimit(opts.RecentLimit, defaultRecentLimit)
	opts.CrossLimit = clampLimit(opts.CrossLimit, defaultCrossLimit)
	opts.RecordsLimit = clampLimit(opts.RecordsLimit, defaultRecordsLimit)
	opts.FactsLimit = clampLimit(opts.FactsLimit, defaultFactsLimit)
	return opts
}

// clampLimit 把 limit 夹到 [1, maxReaderLimit]；非正数用默认值。
func clampLimit(limit, fallback int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > maxReaderLimit {
		return maxReaderLimit
	}
	return limit
}

// Recall 装配一次召回快照。
//
// 顺序固定：Recent → CrossChannel → Records；任一段失败立即返回错误，
// 不返回「部分数据 + nil error」——半份记忆比没有记忆更危险。
func (r *AssembledReader) Recall(ctx context.Context, scope Scope) (Context, error) {
	if ctx == nil {
		return Context{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	if err := ValidateScope(scope); err != nil {
		return Context{}, err
	}

	recent, err := r.source.Recent(ctx, scope, r.opts.RecentLimit)
	if err != nil {
		return Context{}, fmt.Errorf("召回最近会话失败：%w", err)
	}
	cross, err := r.source.CrossChannel(ctx, scope, r.opts.CrossLimit)
	if err != nil {
		return Context{}, fmt.Errorf("召回跨通道消息失败：%w", err)
	}
	records, err := r.source.Records(ctx, scope, r.opts.RecordsLimit)
	if err != nil {
		return Context{}, fmt.Errorf("召回记忆摘要失败：%w", err)
	}

	data := Context{Scope: scope, Recent: recent, CrossChannel: cross, Records: records}
	// 二次校验：即使 repository 的 SQL 有疏漏，越界数据也不能进 Brain
	if err := data.Validate(); err != nil {
		return Context{}, fmt.Errorf("召回数据越界或无效：%w", err)
	}
	return data, nil
}

// Shared 装配当前范围的共享事实快照。
//
// 未注入 FactSource 时返回 Scope 正确、Facts 为 nil 的显式空状态：
// 「没有事实来源」和「查询失败」是两回事，后者必须报错。
func (r *AssembledReader) Shared(ctx context.Context, scope Scope) (SharedState, error) {
	if ctx == nil {
		return SharedState{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return SharedState{}, err
	}
	if err := ValidateScope(scope); err != nil {
		return SharedState{}, err
	}

	var facts []Fact
	if r.facts != nil {
		var err error
		facts, err = r.facts.Facts(ctx, scope, r.opts.FactsLimit)
		if err != nil {
			return SharedState{}, fmt.Errorf("召回共享事实失败：%w", err)
		}
	}

	state := SharedState{Scope: scope, Facts: facts}
	if err := state.Validate(); err != nil {
		return SharedState{}, fmt.Errorf("共享事实越界或无效：%w", err)
	}
	return state, nil
}

// Limits 返回实际生效的窗口大小，便于上层打日志与做预算。
func (r *AssembledReader) Limits() ReaderOptions { return r.opts }
