package database

// 本文件实现 Web 面板的数据访问（只读统计 + 记忆软删除）。
//
// 边界：不返回任何凭据字段；正文由调用方截断，这里原样给出。

import (
	"context"
	"strings"
	"time"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// WebRepo 是面板数据访问实现。
type WebRepo struct{ db *gorm.DB }

// NewWebRepo 注入连接，不自动迁移。
func NewWebRepo(db *gorm.DB) (*WebRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &WebRepo{db: db}, nil
}

// Status 汇总计数与累计费用。
func (r *WebRepo) Status(ctx context.Context) (store.WebStatus, error) {
	if ctx == nil {
		return store.WebStatus{}, store.ErrInvalid
	}
	var st store.WebStatus
	// 注意：不能直接 Scan(&st)——WebStatus 里有 map 字段（MemoryByKind），
	// GORM 解析结构体时会因「unsupported data type: &map[]」中止整行扫描，
	// 导致 events_total 等被静默丢弃。每个查询先用独立小结构体承接再赋值。
	var ev struct {
		EventsTotal   int64
		EventsPending int64
	}
	if err := r.db.WithContext(ctx).Raw(
		"SELECT count(*) AS events_total, count(*) FILTER (WHERE status = 'pending') AS events_pending FROM events",
	).Scan(&ev).Error; err != nil {
		return store.WebStatus{}, &safeError{"统计事件失败", err}
	}
	st.EventsTotal = ev.EventsTotal
	st.EventsPending = ev.EventsPending
	var mem struct{ MemoryTotal int64 }
	if err := r.db.WithContext(ctx).Raw(
		"SELECT count(*) AS memory_total FROM memory_records WHERE valid_to IS NULL AND NOT archived",
	).Scan(&mem).Error; err != nil {
		return store.WebStatus{}, &safeError{"统计记忆失败", err}
	}
	st.MemoryTotal = mem.MemoryTotal
	var kinds []struct {
		Kind  string
		Count int64
	}
	if err := r.db.WithContext(ctx).Raw(
		"SELECT kind, count(*) AS count FROM memory_records WHERE valid_to IS NULL AND NOT archived GROUP BY kind",
	).Scan(&kinds).Error; err != nil {
		return store.WebStatus{}, &safeError{"统计记忆分类失败", err}
	}
	st.MemoryByKind = make(map[string]int64, len(kinds))
	for _, k := range kinds {
		st.MemoryByKind[k.Kind] = k.Count
	}
	var send struct {
		SendTotal  int64
		SendFailed int64
	}
	if err := r.db.WithContext(ctx).Raw(
		"SELECT count(*) AS send_total, count(*) FILTER (WHERE NOT ok) AS send_failed FROM send_log",
	).Scan(&send).Error; err != nil {
		return store.WebStatus{}, &safeError{"统计发送失败", err}
	}
	st.SendTotal, st.SendFailed = send.SendTotal, send.SendFailed
	var cost struct {
		CostUSD float64
	}
	if err := r.db.WithContext(ctx).Raw("SELECT COALESCE(sum(cost_usd),0) AS cost_usd FROM llm_usage").Scan(&cost).Error; err != nil {
		return store.WebStatus{}, &safeError{"统计费用失败", err}
	}
	st.CostUSD = cost.CostUSD
	st.GeneratedAt = time.Now().UTC()
	return st, nil
}

// Memory 按 kind/关键词过滤。
func (r *WebRepo) Memory(ctx context.Context, kind, query string, limit int) ([]store.WebMemory, error) {
	if ctx == nil || limit < 1 || limit > webMaxLimit {
		return nil, store.ErrInvalid
	}
	q := r.db.WithContext(ctx).Table("memory_records").
		Where("valid_to IS NULL AND NOT archived")
	if k := strings.TrimSpace(kind); k != "" {
		q = q.Where("kind = ?", k)
	}
	if kw := strings.TrimSpace(query); kw != "" {
		pattern := "%" + kw + "%"
		q = q.Where("title ILIKE ? OR summary ILIKE ?", pattern, pattern)
	}
	var rows []struct {
		ID         string
		Kind       string
		Title      string
		Summary    string
		Importance float64
		ValidFrom  time.Time
		CreatedAt  time.Time
	}
	if err := q.Select("id, kind, title, summary, importance, valid_from, created_at").
		Order("created_at DESC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, &safeError{"查询记忆失败", err}
	}
	out := make([]store.WebMemory, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.WebMemory{
			ID: row.ID, Kind: row.Kind, Title: row.Title, Summary: row.Summary,
			Importance: row.Importance, ValidFrom: row.ValidFrom, CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// ArchiveMemory 软删除：归档 + 置 valid_to。
func (r *WebRepo) ArchiveMemory(ctx context.Context, id string) error {
	if ctx == nil || strings.TrimSpace(id) == "" {
		return store.ErrInvalid
	}
	res := r.db.WithContext(ctx).Exec(
		"UPDATE memory_records SET archived = TRUE, valid_to = COALESCE(valid_to, now()) WHERE id = ? AND NOT archived", id)
	if res.Error != nil {
		return &safeError{"删除记忆失败", res.Error}
	}
	if res.RowsAffected == 0 {
		return store.ErrWebNotFound
	}
	return nil
}

// Events 取最近事件；dir 仅接受 in/out。
func (r *WebRepo) Events(ctx context.Context, dir string, limit int) ([]store.WebEvent, error) {
	if ctx == nil || limit < 1 || limit > webMaxLimit {
		return nil, store.ErrInvalid
	}
	q := r.db.WithContext(ctx).Table("events")
	switch strings.TrimSpace(dir) {
	case "":
	case "in", "out":
		q = q.Where("dir = ?", strings.TrimSpace(dir))
	default:
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID       string
		Channel  string
		Dir      string
		Session  string
		FromName string
		Text     string
		At       time.Time
		Status   string
	}
	if err := q.Select("id, COALESCE(channel,'') AS channel, dir, COALESCE(session,'') AS session, COALESCE(from_name,'') AS from_name, COALESCE(text,'') AS text, at, status").
		Order("at DESC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, &safeError{"查询事件失败", err}
	}
	out := make([]store.WebEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.WebEvent{
			ID: row.ID, Channel: row.Channel, Dir: row.Dir, Session: row.Session,
			FromName: row.FromName, Text: row.Text, At: row.At, Status: row.Status,
		})
	}
	return out, nil
}

// SendLogs 取最近发送记录。
func (r *WebRepo) SendLogs(ctx context.Context, limit int) ([]store.WebSend, error) {
	if ctx == nil || limit < 1 || limit > webMaxLimit {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		EventID string
		Channel string
		OK      bool
		MsgID   string
		At      time.Time
	}
	if err := r.db.WithContext(ctx).Table("send_log").
		Select("COALESCE(event_id,'') AS event_id, COALESCE(channel,'') AS channel, COALESCE(msg_id,'') AS msg_id, at, ok").
		Order("at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, &safeError{"查询发送记录失败", err}
	}
	out := make([]store.WebSend, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.WebSend{EventID: row.EventID, Channel: row.Channel, OK: row.OK, MsgID: row.MsgID, At: row.At})
	}
	return out, nil
}

// webMaxLimit 是面板单次查询上限（防止一把拉全库）。
const webMaxLimit = 200
