package database

// 本文件为 0.9b 的提炼 worker 提供 DoneSource 实现（0.9c）。
//
// 只做一件事：按 (at, id) 游标扫描 status=done 的事件。
// 具体提炼规则、隐私授权、幂等落库都在 internal/memory/extractworker，这里不含业务判断。

import (
	"context"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory/extractworker"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// doneSourceLimitMax 是单次拉取上限，与 worker 的批次上限保持一致。
const doneSourceLimitMax = 500

// DoneSourceRepo 是 extractworker.DoneSource 的 PG 实现。
type DoneSourceRepo struct{ db *gorm.DB }

// NewDoneSourceRepo 注入既有连接，不自动迁移。
func NewDoneSourceRepo(db *gorm.DB) (*DoneSourceRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &DoneSourceRepo{db: db}, nil
}

// 编译期断言：必须满足 worker 依赖的接口。
var _ extractworker.DoneSource = (*DoneSourceRepo)(nil)

// ListDoneAfter 按 (at, id) 升序返回排在游标之后的已完成事件。
//
//   - 零游标表示从头开始；
//   - 用行值比较 `(at, id) > (?, ?)`，与 worker 的排序键完全一致，避免同 at 事件漏读或重读；
//   - 只按 status=done 过滤，**不做隐私过滤**：私密事件也要被扫描到，
//     是否提炼由 worker 的 AllowPrivate 决定（契约要求隐私判断集中在一处，不要散落）。
func (r *DoneSourceRepo) ListDoneAfter(ctx context.Context, after extractworker.Cursor, limit int) ([]event.Event, error) {
	if ctx == nil || limit < 1 || limit > doneSourceLimitMax {
		return nil, store.ErrInvalid
	}

	query := r.db.WithContext(ctx).Table("events").Where("status = ?", event.StatusDone)
	if !after.IsZero() {
		query = query.Where("(at, id) > (?, ?)", after.At, after.ID)
	}

	var rows []eventRow
	err := query.
		Select("id,channel,dir,session,from_id,from_name,text,private,reply_to,at,status,meta").
		Order("at ASC, id ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, &safeError{"扫描待提炼事件失败", err}
	}

	out := make([]event.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.event())
	}
	return out, nil
}
