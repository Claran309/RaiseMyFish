package database

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// memoryRecordLimitMax 是运维查询的单次上限，避免误把整表读进内存。
	memoryRecordLimitMax = 200
	// memorySearchMaxRunes 限制搜索词长度，防止构造超长 SQL 参数。
	memorySearchMaxRunes = 200
)

// MemoryRepo 执行范围受限的持久化读写，不实现提炼/Reader编排。
type MemoryRepo struct{ db *gorm.DB }

// 编译期断言：事实查询同时满足 store 与 memory 两侧的窄接口。
var (
	_ store.MemoryRepo  = (*MemoryRepo)(nil)
	_ store.FactSource  = (*MemoryRepo)(nil)
	_ memory.FactSource = (*MemoryRepo)(nil)
)

// NewMemoryRepo 复用infra连接。
func NewMemoryRepo(db *gorm.DB) (*MemoryRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &MemoryRepo{db: db}, nil
}

// InsertRecord 不覆盖已有ID的记录。
func (r *MemoryRepo) InsertRecord(ctx context.Context, m store.MemoryRecord) (bool, error) {
	if ctx == nil {
		return false, store.ErrInvalid
	}
	if err := m.Validate(); err != nil {
		return false, err
	}
	ids, err := json.Marshal(m.EventIDs)
	if err != nil {
		return false, &safeError{"编码记忆来源失败", err}
	}
	// 数组通过JSON参数转TEXT[]，避免拼接事件ID，也不引入另一套PG驱动。
	result := r.db.WithContext(ctx).Exec(`INSERT INTO memory_records
 (id,source,kind,title,summary,body,importance,confidence,private,channel,session,valid_from,event_ids)
 VALUES (?,?,?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,ARRAY(SELECT jsonb_array_elements_text(NULLIF(?::jsonb,'null'::jsonb))))
 ON CONFLICT (id) DO NOTHING`, m.ID, m.Source, m.Kind, m.Title, m.Summary, m.Body, m.Importance, m.Confidence, m.Private, m.Channel, m.Session, m.ValidFrom, string(ids))
	if result.Error != nil {
		return false, &safeError{"写入记忆失败", result.Error}
	}
	return result.RowsAffected == 1, nil
}

// Recent 返回最近N条同会话消息，展示顺序为时间和ID升序。
func (r *MemoryRepo) Recent(ctx context.Context, s memory.Scope, limit int) ([]memory.Message, error) {
	if err := validateRecall(ctx, s, limit); err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Table("events").Where("channel=? AND session=?", s.Channel, s.Session).Where("(dir <> 'out' OR status='done')")
	if !s.Private {
		query = query.Where("NOT private")
	}
	// 即使旧数据被错误降密，也不允许DM公开行进入上下文。
	query = query.Where("(channel <> 'dm' OR private)")
	return recallMessages(query, s, limit, false)
}

// CrossChannel 只读其他通道的公开消息。
func (r *MemoryRepo) CrossChannel(ctx context.Context, s memory.Scope, limit int) ([]memory.Message, error) {
	if err := validateRecall(ctx, s, limit); err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Table("events").Where("channel <> ? AND channel <> 'dm' AND NOT private", s.Channel).Where("(dir <> 'out' OR status='done')")
	return recallMessages(query, s, limit, true)
}

// Records 只返回有效的可见摘要，不返回Body。
func (r *MemoryRepo) Records(ctx context.Context, s memory.Scope, limit int) ([]memory.Record, error) {
	if err := validateRecall(ctx, s, limit); err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Table("memory_records").Where("valid_to IS NULL AND NOT archived AND valid_from <= now()")
	if s.Private {
		query = query.Where("(NOT private OR (private AND channel=? AND session=?))", s.Channel, s.Session)
	} else {
		query = query.Where("NOT private")
	}
	query = query.Where("(channel IS NULL OR channel <> 'dm' OR private)")
	// SQL行与领域DTO分离，GORM不能直接映射DTO的[]string。
	var rows []struct {
		ID         string
		Channel    event.Channel
		Session    string
		Summary    string
		Importance float64
		Confidence float64
		Private    bool
		ValidFrom  time.Time
		IDs        json.RawMessage
	}
	err := query.Select("id, channel, session, summary, importance, confidence, private, valid_from, to_json(event_ids) AS ids").Order("importance DESC, id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"查询记忆摘要失败", err}
	}
	result := make([]memory.Record, 0, len(rows))
	for _, row := range rows {
		record := memory.Record{ID: row.ID, Channel: row.Channel, Session: row.Session, Summary: row.Summary, Importance: row.Importance, Confidence: row.Confidence, Private: row.Private, ValidFrom: row.ValidFrom}
		if len(row.IDs) > 0 {
			if err := json.Unmarshal(row.IDs, &record.EventIDs); err != nil {
				return nil, &safeError{"读取记忆来源失败", err}
			}
		}
		result = append(result, record)
	}
	if err := (memory.Context{Scope: s, Records: result}).Validate(); err != nil {
		return nil, &safeError{"记忆摘要存量数据无效", err}
	}
	return result, nil
}

// ListRecords 返回最近创建的记录（含私密与归档行）。
//
// 这是本机运维查询，不做公开/私密过滤：fishctl 由主人本人在本机执行，
// 需要能看到完整存量来排查提炼问题；给 Brain 的召回仍走 Records/Facts 的隐私过滤。
func (r *MemoryRepo) ListRecords(ctx context.Context, limit int) ([]store.MemoryRecord, error) {
	if err := validateMemoryRecordQuery(ctx, limit); err != nil {
		return nil, err
	}
	return r.queryMemoryRecords(ctx, "", limit)
}

// SearchRecords 按 title/summary 模糊搜索，关键词与 limit 都走参数化/边界校验。
func (r *MemoryRepo) SearchRecords(ctx context.Context, q string, limit int) ([]store.MemoryRecord, error) {
	if err := validateMemoryRecordQuery(ctx, limit); err != nil {
		return nil, err
	}
	keyword := strings.TrimSpace(q)
	if keyword == "" || utf8.RuneCountInString(keyword) > memorySearchMaxRunes {
		return nil, store.ErrInvalid
	}
	return r.queryMemoryRecords(ctx, keyword, limit)
}

// CountByKind 按 kind 统计全部记录数量（含私密与归档行）。
func (r *MemoryRepo) CountByKind(ctx context.Context) (map[string]int, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		Kind  string
		Count int64
	}
	err := r.db.WithContext(ctx).Table("memory_records").
		Select("kind, COUNT(*) AS count").
		Group("kind").
		Order("kind ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"统计记忆类型失败", err}
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.Kind] = int(row.Count)
	}
	return counts, nil
}

// queryMemoryRecords 统一 list/search 的字段投影与排序，关键词为空表示不筛选。
func (r *MemoryRepo) queryMemoryRecords(ctx context.Context, keyword string, limit int) ([]store.MemoryRecord, error) {
	query := r.db.WithContext(ctx).Table("memory_records")
	if keyword != "" {
		pattern := "%" + keyword + "%"
		query = query.Where("title ILIKE ? OR summary ILIKE ?", pattern, pattern)
	}
	// channel/session 可为 NULL（全局记录）：先归一成空串，避免扫描 NULL 失败。
	var rows []struct {
		ID         string
		Source     string
		Kind       string
		Title      string
		Summary    string
		Body       string
		Importance float64
		Confidence float64
		Private    bool
		Channel    event.Channel
		Session    string
		ValidFrom  time.Time
		CreatedAt  time.Time
		IDs        json.RawMessage
	}
	err := query.
		Select("id, source, kind, title, summary, body, importance, confidence, private, COALESCE(channel,'') AS channel, COALESCE(session,'') AS session, valid_from, created_at, to_json(event_ids) AS ids").
		Order("created_at DESC, id ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, &safeError{"查询记忆记录失败", err}
	}

	records := make([]store.MemoryRecord, 0, len(rows))
	for _, row := range rows {
		record := store.MemoryRecord{
			Record: memory.Record{
				ID:         row.ID,
				Channel:    row.Channel,
				Session:    row.Session,
				Summary:    row.Summary,
				Importance: row.Importance,
				Confidence: row.Confidence,
				Private:    row.Private,
				ValidFrom:  row.ValidFrom,
			},
			Source:    row.Source,
			Kind:      row.Kind,
			Title:     row.Title,
			Body:      row.Body,
			CreatedAt: row.CreatedAt,
		}
		if len(row.IDs) > 0 {
			if err := json.Unmarshal(row.IDs, &record.EventIDs); err != nil {
				return nil, &safeError{"读取记忆来源失败", err}
			}
		}
		records = append(records, record)
	}
	return records, nil
}

func validateMemoryRecordQuery(ctx context.Context, limit int) error {
	if ctx == nil || limit < 1 || limit > memoryRecordLimitMax {
		return store.ErrInvalid
	}
	return nil
}

// Facts 返回 scope 可见的当前有效共享事实（0.9d）。
//
// 事实没有独立表：复用 memory_records 里 kind='fact' 的记录——
// Key=title、Value=summary、SourceID=event_ids[0]（缺失时退回 source）、At=valid_from。
// 这样不需要新迁移，也不会出现第二套事实存储与记录不一致。
//
// 缺少 channel/session 的行按契约「未观测的事实省略」处理，直接不返回；
// 返回前用 memory.SharedState.Validate 再核一遍范围与字段。
func (r *MemoryRepo) Facts(ctx context.Context, s memory.Scope, limit int) ([]memory.Fact, error) {
	if err := validateRecall(ctx, s, limit); err != nil {
		return nil, err
	}

	query := r.db.WithContext(ctx).Table("memory_records").
		Where("kind = ?", "fact").
		Where("valid_to IS NULL AND NOT archived AND valid_from <= now()").
		Where("channel IS NOT NULL AND channel <> '' AND session IS NOT NULL AND session <> ''")
	if s.Private {
		query = query.Where("(NOT private OR (private AND channel=? AND session=?))", s.Channel, s.Session)
	} else {
		query = query.Where("NOT private")
	}
	// 即使旧数据被错误降密，也不允许 DM 行进入公开视角
	query = query.Where("(channel <> 'dm' OR private)")

	var rows []struct {
		Title     string
		Summary   string
		Channel   event.Channel
		Session   string
		Private   bool
		ValidFrom time.Time
		Source    string
		IDs       json.RawMessage
	}
	err := query.
		Select("title, summary, channel, session, private, valid_from, source, to_json(event_ids) AS ids").
		Order("title ASC, valid_from ASC, id ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, &safeError{"查询共享事实失败", err}
	}

	facts := make([]memory.Fact, 0, len(rows))
	for _, row := range rows {
		sourceID := strings.TrimSpace(row.Source)
		if len(row.IDs) > 0 {
			var ids []string
			if err := json.Unmarshal(row.IDs, &ids); err != nil {
				return nil, &safeError{"读取事实来源失败", err}
			}
			if len(ids) > 0 && strings.TrimSpace(ids[0]) != "" {
				sourceID = ids[0]
			}
		}
		facts = append(facts, memory.Fact{
			Key:      row.Title,
			Value:    row.Summary,
			Channel:  row.Channel,
			Session:  row.Session,
			Private:  row.Private,
			SourceID: sourceID,
			At:       row.ValidFrom,
		})
	}

	// 契约要求顺序为 Key → At → SourceID；SQL 已按前两列排，这里补最后一列保证确定
	slices.SortStableFunc(facts, func(a, b memory.Fact) int {
		if c := strings.Compare(a.Key, b.Key); c != 0 {
			return c
		}
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(a.SourceID, b.SourceID)
	})

	if err := (memory.SharedState{Scope: s, Facts: facts}).Validate(); err != nil {
		return nil, &safeError{"共享事实存量数据无效", err}
	}
	return facts, nil
}

func validateRecall(ctx context.Context, s memory.Scope, limit int) error {
	if ctx == nil || limit < 1 || limit > 100 {
		return store.ErrInvalid
	}
	if err := memory.ValidateScope(s); err != nil {
		return fmt.Errorf("召回范围无效：%w", store.ErrInvalid)
	}
	return nil
}

func recallMessages(query *gorm.DB, s memory.Scope, limit int, cross bool) ([]memory.Message, error) {
	var messages []memory.Message
	err := query.Select("id AS event_id,channel,session,dir,from_id,from_name,text,private,at").Order("at DESC, id DESC").Limit(limit).Find(&messages).Error
	if err != nil {
		return nil, &safeError{"查询记忆事件失败", err}
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	data := memory.Context{Scope: s, Recent: messages}
	if cross {
		data.Recent = nil
		data.CrossChannel = messages
	}
	if err := data.Validate(); err != nil {
		return nil, &safeError{"记忆事件存量数据无效", err}
	}
	return messages, nil
}
