package store

// 本文件定义观众档案（viewer_profiles）的仓储契约。
//
// 档案是「认人」用的投影：uid 是主键（昵称会变），hits/replies 是累计计数，
// recent_* 是最近若干条原文快照。

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ViewerProfile 是一份观众档案。
type ViewerProfile struct {
	// UID 是平台用户 ID（主键）。
	UID string
	// Name 是最近一次见到的昵称快照。
	Name string
	// Hits 是说过几句（in 事件数）。
	Hits int
	// Replies 是鱼回了几次（out 事件数）。
	Replies int
	// LastSeen 是最后出现时间。
	LastSeen time.Time
	// RecentTexts 是最近说过的话（新的在前）。
	RecentTexts []string
	// RecentReplies 是鱼最近回过的话（新的在前）。
	RecentReplies []string
	// UpdatedAt 是档案写入时间。
	UpdatedAt time.Time
}

// Validate 只做结构性检查，不猜业务含义。
func (p ViewerProfile) Validate() error {
	if strings.TrimSpace(p.UID) == "" || strings.TrimSpace(p.Name) == "" {
		return ErrInvalid
	}
	if p.Hits < 0 || p.Replies < 0 || p.LastSeen.IsZero() {
		return ErrInvalid
	}
	return nil
}

// ViewerRepo 是观众档案的读写接口。
type ViewerRepo interface {
	// UpsertProfile 写入或更新一份档案（按 uid 覆盖）。
	UpsertProfile(ctx context.Context, p ViewerProfile) error
	// GetProfile 按 uid 读档案；不存在返回 ErrNotFound。
	GetProfile(ctx context.Context, uid string) (ViewerProfile, error)
	// TopViewers 按 hits 倒序取前 limit 个档案。
	TopViewers(ctx context.Context, limit int) ([]ViewerProfile, error)
}

// ViewerRebuildOptions 是档案重建参数。
type ViewerRebuildOptions struct {
	MinHits       int
	RecentTexts   int
	RecentReplies int
}

// ViewerRebuildStats 是一轮重建的统计。
type ViewerRebuildStats struct {
	Profiles int
}

// ErrViewerNotFound 用统一错误表达「没有这个档案」（陌生人）。
var ErrViewerNotFound = errors.New("观众档案不存在")
