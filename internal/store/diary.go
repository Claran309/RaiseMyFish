package store

// 每日总结的读写接口：生成方按天覆盖，面板只读与导出。

import (
	"context"
	"errors"
	"time"
)

// ErrDiaryNotFound 表示当天还没有总结。
var ErrDiaryNotFound = errors.New("日报不存在")

// DiaryEvent 是生成日报时的一条事件投影（含私信，日报只在本机面板查看）。
type DiaryEvent struct {
	ID       string
	Channel  string
	Dir      string
	FromName string
	Text     string
	At       time.Time
	Private  bool
}

// DailySummary 是一天的总结；Day 为上海时区的 YYYY-MM-DD。
type DailySummary struct {
	Day        string
	Content    string
	Model      string
	EventCount int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DiaryRepo 是日报存储与当天事件来源。
type DiaryRepo interface {
	// DayEvents 取 [from,to) 的事件，新的在前，最多 limit 条。
	DayEvents(ctx context.Context, from, to time.Time, limit int) ([]DiaryEvent, error)
	// SaveDailySummary 按天 upsert，重复生成覆盖旧内容。
	SaveDailySummary(ctx context.Context, summary DailySummary) error
	// DailySummary 取某天日报；不存在时返回 ErrDiaryNotFound。
	DailySummary(ctx context.Context, day string) (DailySummary, error)
	// DailySummaries 取最近日报，新的在前。
	DailySummaries(ctx context.Context, limit int) ([]DailySummary, error)
}
