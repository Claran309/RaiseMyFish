package database

// 每日总结仓储：事件按天取数，日报按天 upsert。

import (
	"context"
	"strings"
	"time"

	"RaiseMyFish/internal/store"
)

// diaryMaxEvents 是单次生成允许读取的事件上限，防止历史回放一次拉全库。
const diaryMaxEvents = 2000

// DayEvents 取 [from,to) 的事件，新的在前，最多 limit 条。
func (r *WebRepo) DayEvents(ctx context.Context, from, to time.Time, limit int) ([]store.DiaryEvent, error) {
	if ctx == nil || !from.Before(to) || limit < 1 || limit > diaryMaxEvents {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID       string
		Channel  string
		Dir      string
		FromName string
		Text     string
		At       time.Time
		Private  bool
	}
	if err := r.db.WithContext(ctx).Table("events").
		Select("id, COALESCE(channel,'') AS channel, dir, COALESCE(from_name,'') AS from_name, COALESCE(text,'') AS text, at, private").
		Where("at >= ? AND at < ?", from, to).
		Order("at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, &safeError{"查询日报事件失败", err}
	}
	out := make([]store.DiaryEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.DiaryEvent{
			ID: row.ID, Channel: row.Channel, Dir: row.Dir,
			FromName: row.FromName, Text: row.Text, At: row.At, Private: row.Private,
		})
	}
	return out, nil
}

// validDiaryDay 只接受 YYYY-MM-DD。
func validDiaryDay(day string) bool {
	_, err := time.Parse("2006-01-02", strings.TrimSpace(day))
	return err == nil
}

// SaveDailySummary 按天 upsert；重复生成覆盖旧内容与事件数。
func (r *WebRepo) SaveDailySummary(ctx context.Context, summary store.DailySummary) error {
	if ctx == nil || !validDiaryDay(summary.Day) || strings.TrimSpace(summary.Content) == "" {
		return store.ErrInvalid
	}
	err := r.db.WithContext(ctx).Exec(
		`INSERT INTO daily_summaries(day, content, model, event_count) VALUES (?::date, ?, ?, ?)
		 ON CONFLICT (day) DO UPDATE SET
		   content = EXCLUDED.content,
		   model = EXCLUDED.model,
		   event_count = EXCLUDED.event_count,
		   updated_at = now()`,
		summary.Day, summary.Content, summary.Model, summary.EventCount,
	).Error
	if err != nil {
		return &safeError{"保存日报失败", err}
	}
	return nil
}

// DailySummary 取某天日报；不存在时返回 ErrDiaryNotFound。
func (r *WebRepo) DailySummary(ctx context.Context, day string) (store.DailySummary, error) {
	if ctx == nil || !validDiaryDay(day) {
		return store.DailySummary{}, store.ErrInvalid
	}
	var rows []struct {
		Day        string
		Content    string
		Model      string
		EventCount int
		CreatedAt  time.Time
		UpdatedAt  time.Time
	}
	if err := r.db.WithContext(ctx).Table("daily_summaries").
		Select("to_char(day,'YYYY-MM-DD') AS day, content, model, event_count, created_at, updated_at").
		Where("day = ?::date", day).Limit(1).Find(&rows).Error; err != nil {
		return store.DailySummary{}, &safeError{"查询日报失败", err}
	}
	if len(rows) == 0 {
		return store.DailySummary{}, store.ErrDiaryNotFound
	}
	row := rows[0]
	return store.DailySummary{
		Day: row.Day, Content: row.Content, Model: row.Model, EventCount: row.EventCount,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

// DailySummaries 取最近日报，新的在前。
func (r *WebRepo) DailySummaries(ctx context.Context, limit int) ([]store.DailySummary, error) {
	if ctx == nil || limit < 1 || limit > webMaxLimit {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		Day        string
		Content    string
		Model      string
		EventCount int
		CreatedAt  time.Time
		UpdatedAt  time.Time
	}
	if err := r.db.WithContext(ctx).Table("daily_summaries").
		Select("to_char(day,'YYYY-MM-DD') AS day, content, model, event_count, created_at, updated_at").
		Order("day DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, &safeError{"查询日报列表失败", err}
	}
	out := make([]store.DailySummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.DailySummary{
			Day: row.Day, Content: row.Content, Model: row.Model, EventCount: row.EventCount,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}

// 编译期断言：面板数据源同时提供注入与日报能力。
var (
	_ store.InjectionRepo = (*WebRepo)(nil)
	_ store.DiaryRepo     = (*WebRepo)(nil)
)
