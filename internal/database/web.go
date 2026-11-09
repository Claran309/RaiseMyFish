package database

// 本文件实现 Web 面板的数据访问（只读统计 + 记忆软删除）。
//
// 边界：不返回任何凭据字段；正文由调用方截断，这里原样给出。

import (
	"context"
	"math"
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

// Status 汇总计数与费用（区分已计价与未知）。
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
	// 保留 SUM 的 NULL 语义：全无报价时 known_cost 为 nil，不能 COALESCE 成 0。
	// 同时统计总条数与未知条数，调用方才能区分空账本、真实零、全未知与混合。
	var usage struct {
		KnownCost    *float64
		UnknownCalls int64
		TotalCalls   int64
	}
	if err := r.db.WithContext(ctx).Raw(
		`SELECT sum(cost_usd) AS known_cost,
		        count(*) FILTER (WHERE cost_usd IS NULL) AS unknown_calls,
		        count(*) AS total_calls
		   FROM llm_usage`,
	).Scan(&usage).Error; err != nil {
		return store.WebStatus{}, &safeError{"统计费用失败", err}
	}
	st.KnownCostUSD = usage.KnownCost
	st.UnknownCalls = usage.UnknownCalls
	st.TotalCalls = usage.TotalCalls
	if usage.TotalCalls > 0 {
		st.Coverage = float64(usage.TotalCalls-usage.UnknownCalls) / float64(usage.TotalCalls)
	}
	st.GeneratedAt = time.Now().UTC()
	return st, nil
}

// Memory 按 kind/关键词过滤公开记忆。
//
// 面板记忆页是公开导出视角：私密 DM 摘要与其错误降密行都不展示。
func (r *WebRepo) Memory(ctx context.Context, kind, query string, limit int) ([]store.WebMemory, error) {
	if ctx == nil || limit < 1 || limit > webMaxLimit {
		return nil, store.ErrInvalid
	}
	q := r.db.WithContext(ctx).Table("memory_records").
		Where("valid_to IS NULL AND NOT archived").
		Where("NOT private AND (channel IS NULL OR channel <> 'dm' OR private)")
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
			// 展示用：保留两位小数，避免 0.6000000000000001 这种
			Importance: round2(row.Importance), ValidFrom: row.ValidFrom, CreatedAt: row.CreatedAt,
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

// Events 取最近公开事件；dir 仅接受 in/out。
//
// 活动流水是公开视角：私密事件和错误降密的 DM 都不出现。
func (r *WebRepo) Events(ctx context.Context, dir string, limit int) ([]store.WebEvent, error) {
	if ctx == nil || limit < 1 || limit > webMaxLimit {
		return nil, store.ErrInvalid
	}
	q := r.db.WithContext(ctx).Table("events").
		Where("NOT private AND channel <> ?", "dm")
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

// EventsByChannel 按通道取最近事件；channel 只接受 live/comment/dm。
//
// live/comment 是公开通道，只返回公开行；dm 是显式的私密通道视图
// （仅面板本机可见），按 channel 精确隔离，不影响公开视角。
func (r *WebRepo) EventsByChannel(ctx context.Context, channel, dir string, limit int) ([]store.WebEvent, error) {
	if ctx == nil || limit < 1 || limit > webMaxLimit {
		return nil, store.ErrInvalid
	}
	switch channel {
	case "live", "comment":
	case "dm":
	default:
		return nil, store.ErrInvalid
	}
	q := r.db.WithContext(ctx).Table("events").Where("channel = ?", channel)
	if channel != "dm" {
		q = q.Where("NOT private")
	}
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
		return nil, &safeError{"查询通道事件失败", err}
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

// Event 取单条公开事件详情；回复计划与尝试是可选的（事件可能还没生成回复）。
//
// 活动页只链接公开事件；私密事件和错误降密的 DM 不能经 id 直读。
func (r *WebRepo) Event(ctx context.Context, id string) (store.WebEventDetail, error) {
	if ctx == nil || strings.TrimSpace(id) == "" {
		return store.WebEventDetail{}, store.ErrInvalid
	}
	var row struct {
		ID        string
		Channel   string
		Dir       string
		Session   string
		FromID    string
		FromName  string
		Text      string
		Private   bool
		ReplyTo   string
		At        time.Time
		Status    string
		Meta      string
		CreatedAt time.Time
	}
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, COALESCE(channel,'') AS channel, dir, COALESCE(session,'') AS session,
		        COALESCE(from_id,'') AS from_id, COALESCE(from_name,'') AS from_name,
		        COALESCE(text,'') AS text, private, COALESCE(reply_to,'') AS reply_to,
		        at, status, COALESCE(meta::text,'') AS meta, created_at
		   FROM events WHERE id = ? AND NOT private AND (channel <> 'dm' OR private)`, id,
	).Scan(&row).Error
	if err != nil {
		return store.WebEventDetail{}, &safeError{"查询事件详情失败", err}
	}
	if row.ID == "" {
		return store.WebEventDetail{}, store.ErrWebNotFound
	}
	detail := store.WebEventDetail{
		WebEvent: store.WebEvent{
			ID: row.ID, Channel: row.Channel, Dir: row.Dir, Session: row.Session,
			FromName: row.FromName, Text: row.Text, At: row.At, Status: row.Status,
		},
		FromID: row.FromID, ReplyTo: row.ReplyTo, Private: row.Private,
		Meta: row.Meta, CreatedAt: row.CreatedAt,
	}
	var plan struct {
		ActorID      string
		RoomID       string
		Text         string
		State        string
		AttemptCount int
		CreatedAt    time.Time
	}
	err = r.db.WithContext(ctx).Raw(
		`SELECT actor_id, room_id, text, state, attempt_count, created_at
		   FROM send_plans WHERE event_id = ?`, id,
	).Scan(&plan).Error
	if err != nil {
		return store.WebEventDetail{}, &safeError{"查询回复计划失败", err}
	}
	if plan.ActorID == "" && plan.RoomID == "" && plan.Text == "" {
		return detail, nil
	}
	reply := store.WebReply{
		ActorID: plan.ActorID, RoomID: plan.RoomID, Text: plan.Text,
		State: plan.State, AttemptCount: plan.AttemptCount, CreatedAt: plan.CreatedAt,
	}
	var attempts []struct {
		Number    int
		State     string
		OK        *bool
		Verified  bool
		MsgID     string
		Evidence  string
		ErrorKind string
		CreatedAt time.Time
	}
	if err := r.db.WithContext(ctx).Raw(
		`SELECT number, state, ok, verified, COALESCE(msg_id,'') AS msg_id,
		        COALESCE(evidence,'') AS evidence, COALESCE(error_kind,'') AS error_kind, created_at
		   FROM send_attempts WHERE event_id = ? ORDER BY number ASC`, id,
	).Scan(&attempts).Error; err != nil {
		return store.WebEventDetail{}, &safeError{"查询发送尝试失败", err}
	}
	reply.Attempts = make([]store.WebAttempt, 0, len(attempts))
	for _, a := range attempts {
		reply.Attempts = append(reply.Attempts, store.WebAttempt{
			Number: a.Number, State: a.State, OK: a.OK, Verified: a.Verified,
			MsgID: a.MsgID, Evidence: a.Evidence, ErrorKind: a.ErrorKind, CreatedAt: a.CreatedAt,
		})
	}
	detail.Reply = &reply
	return detail, nil
}

// Errors 取最近公开异常事件：status 为 dead/retry 的（按时间倒序）。
//
// 异常列表同样是公开视角，不能借失败状态导出私密正文。
func (r *WebRepo) Errors(ctx context.Context, limit int) ([]store.WebEvent, error) {
	if ctx == nil || limit < 1 || limit > webMaxLimit {
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
	if err := r.db.WithContext(ctx).Table("events").
		Where("status IN ?", []string{"dead", "retry"}).
		Where("NOT private AND channel <> ?", "dm").
		Select("id, COALESCE(channel,'') AS channel, dir, COALESCE(session,'') AS session, COALESCE(from_name,'') AS from_name, COALESCE(text,'') AS text, at, status").
		Order("at DESC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, &safeError{"查询异常事件失败", err}
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

// round2 保留两位小数（展示用）。
func round2(v float64) float64 { return math.Round(v*100) / 100 }

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
