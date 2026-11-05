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

// InsertRecord 写入记录；同 ID 只在「同类事实 + 同范围 + 时间不倒退」时更新。
//
// 返回 false 表示没有新增也没有更新：既包括内容幂等，也包括冲突被拒绝
// （范围不一致、非事实类、旧事件晚到）。拒绝时原行保持不动，绝不跨范围覆盖。
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
	//
	// 冲突更新的四道闸门（缺一不可）：
	//  1. 新旧都必须是 fact（非事实类仍保持按事件幂等）；
	//  2. private/channel/session 必须完全一致，公共行与 DM-A、DM-B 互不覆盖；
	//  3. 新记录的 valid_from 必须更晚，逆序补提炼的旧事件不能盖掉新事实；
	//  4. 摘要确实变化才更新，重复提炼保持幂等。
	// valid_from 取 EXCLUDED 的事件时间而不是 now()：否则「更新时间」会顶替
	// 「事实发生时间」，让第 3 道的先后比较失真。
	result := r.db.WithContext(ctx).Exec(`INSERT INTO memory_records
(id,source,kind,title,summary,body,importance,confidence,private,channel,session,valid_from,event_ids,from_id,from_name)
VALUES (?,?,?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,ARRAY(SELECT jsonb_array_elements_text(NULLIF(?::jsonb,'null'::jsonb))),NULLIF(?,''),NULLIF(?,''))
ON CONFLICT (id) DO UPDATE SET
  summary    = EXCLUDED.summary,
  confidence = EXCLUDED.confidence,
  valid_from = EXCLUDED.valid_from,
  event_ids  = EXCLUDED.event_ids,
  from_id    = COALESCE(EXCLUDED.from_id, memory_records.from_id),
  from_name  = COALESCE(EXCLUDED.from_name, memory_records.from_name)
WHERE memory_records.kind = 'fact'
  AND EXCLUDED.kind = 'fact'
  AND memory_records.private = EXCLUDED.private
  AND COALESCE(memory_records.channel,'') = COALESCE(EXCLUDED.channel,'')
  AND COALESCE(memory_records.session,'') = COALESCE(EXCLUDED.session,'')
  AND EXCLUDED.valid_from > memory_records.valid_from
  AND memory_records.summary IS DISTINCT FROM EXCLUDED.summary`, m.ID, m.Source, m.Kind, m.Title, m.Summary, m.Body, m.Importance, m.Confidence, m.Private, m.Channel, m.Session, m.ValidFrom, string(ids), m.FromID, m.FromName)
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
	query := memoryScopeQuery(r.db.WithContext(ctx), s)
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

// memoryScopeQuery 统一 Records/KeywordRecords 的有效期与公开私密过滤。
func memoryScopeQuery(db *gorm.DB, s memory.Scope) *gorm.DB {
	query := db.Table("memory_records").Where("valid_to IS NULL AND NOT archived AND valid_from <= now()")
	if s.Private {
		query = query.Where("(NOT private OR (private AND channel=? AND session=?))", s.Channel, s.Session)
	} else {
		query = query.Where("NOT private")
	}
	// 即使旧数据被错误降密，也不允许 DM 行进入公开视角。
	return query.Where("(channel IS NULL OR channel <> 'dm' OR private)")
}

// memoryVisibilitySQL 是语义检索手写 SQL 的同口径条件与参数。
func memoryVisibilitySQL(s memory.Scope) (string, []any) {
	parts := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if s.Private {
		parts = append(parts, "(NOT private OR (private AND channel=? AND session=?))")
		args = append(args, string(s.Channel), s.Session)
	} else {
		parts = append(parts, "NOT private")
	}
	parts = append(parts, "(channel IS NULL OR channel <> 'dm' OR private)")
	return strings.Join(parts, " AND "), args
}

// memoryRecordRow 是关键词/语义两路共用的摘要投影。
type memoryRecordRow struct {
	ID         string
	Channel    event.Channel
	Session    string
	Summary    string
	Importance float64
	Confidence float64
	Private    bool
	ValidFrom  time.Time
	IDs        json.RawMessage
	Sim        float64
}

func (row memoryRecordRow) record() (memory.Record, error) {
	record := memory.Record{
		ID: row.ID, Channel: row.Channel, Session: row.Session, Summary: row.Summary,
		Importance: row.Importance, Confidence: row.Confidence, Private: row.Private,
		ValidFrom: row.ValidFrom,
	}
	if len(row.IDs) > 0 {
		if err := json.Unmarshal(row.IDs, &record.EventIDs); err != nil {
			return memory.Record{}, err
		}
	}
	return record, nil
}

// KeywordRecords 在 scope 可见范围内按 title/summary 做关键词模糊匹配。
func (r *MemoryRepo) KeywordRecords(ctx context.Context, s memory.Scope, keyword string, limit int) ([]memory.Record, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, store.ErrInvalid
	}
	if err := validateRecall(ctx, s, limit); err != nil {
		return nil, err
	}
	pattern := "%" + keyword + "%"
	var rows []memoryRecordRow
	err := memoryScopeQuery(r.db.WithContext(ctx), s).
		Where("(title ILIKE ? OR summary ILIKE ?)", pattern, pattern).
		Select("id, channel, session, summary, importance, confidence, private, valid_from, to_json(event_ids) AS ids").
		Order("importance DESC, valid_from DESC, id ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, &safeError{"关键词检索记忆失败", err}
	}
	return memoryRows(rows)
}

// KeywordMessages 在 scope 可见范围内按正文/昵称匹配原始弹幕。
//
// 记忆摘要可能还没提炼出来，但 events 已有原文；这一路保证「数据在就能搜到」。
// 可见性口径与 Recent 完全一致（会话隔离、私密过滤、DM 不外泄）。
func (r *MemoryRepo) KeywordMessages(ctx context.Context, s memory.Scope, keyword string, limit int) ([]memory.Message, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, store.ErrInvalid
	}
	if utf8.RuneCountInString(keyword) > memorySearchMaxRunes {
		return nil, store.ErrInvalid
	}
	if err := validateRecall(ctx, s, limit); err != nil {
		return nil, err
	}
	pattern := "%" + keyword + "%"
	query := r.db.WithContext(ctx).Table("events").Where("channel=? AND session=?", s.Channel, s.Session).Where("(dir <> 'out' OR status='done')")
	if !s.Private {
		query = query.Where("NOT private")
	}
	query = query.Where("(channel <> 'dm' OR private)")
	query = query.Where("(text ILIKE ? OR from_name ILIKE ?)", pattern, pattern)
	return recallMessages(query, s, limit, false)
}

// SemanticRecords 在 scope 可见范围内按余弦距离取最相近的摘要。
func (r *MemoryRepo) SemanticRecords(ctx context.Context, s memory.Scope, vector []float64, limit int) ([]memory.SemanticRecord, error) {
	if err := validateRecall(ctx, s, limit); err != nil {
		return nil, err
	}
	literal, err := vectorLiteral(vector, memory.EmbeddingDimension)
	if err != nil {
		return nil, store.ErrInvalid
	}
	visibility, args := memoryVisibilitySQL(s)
	params := make([]any, 0, len(args)+2)
	params = append(params, literal)
	params = append(params, args...)
	params = append(params, limit)
	var rows []memoryRecordRow
	err = r.db.WithContext(ctx).Raw(`
WITH q AS (SELECT ?::vector AS v)
SELECT id, channel, session, summary, importance, confidence, private, valid_from,
       to_json(event_ids) AS ids, 1 - (embedding <=> q.v) AS sim
FROM memory_records, q
WHERE valid_to IS NULL AND NOT archived AND valid_from <= now()
  AND embedding IS NOT NULL AND `+visibility+`
ORDER BY embedding <=> q.v, id ASC
LIMIT ?`, params...).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"向量检索记忆失败", err}
	}
	records := make([]memory.SemanticRecord, 0, len(rows))
	for _, row := range rows {
		record, err := row.record()
		if err != nil {
			return nil, &safeError{"解析记忆来源失败", err}
		}
		records = append(records, memory.SemanticRecord{Record: record, Similarity: row.Sim})
	}
	return records, nil
}

func memoryRows(rows []memoryRecordRow) ([]memory.Record, error) {
	records := make([]memory.Record, 0, len(rows))
	for _, row := range rows {
		record, err := row.record()
		if err != nil {
			return nil, &safeError{"解析记忆来源失败", err}
		}
		records = append(records, record)
	}
	return records, nil
}

// MemoryEmbeddingInputs 返回全部非空摘要的向量化源字段。
//
// 本机运维任务视角：不按 visibility 过滤，召回查询仍会在 SQL 层做范围过滤。
func (r *MemoryRepo) MemoryEmbeddingInputs(ctx context.Context) ([]memory.EmbeddingInput, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID      string
		Title   string
		Summary string
	}
	err := r.db.WithContext(ctx).Table("memory_records").
		Select("id, COALESCE(title,'') AS title, COALESCE(summary,'') AS summary").
		Where("COALESCE(summary,'') <> ''").
		Order("id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"读取记忆向量化源失败", err}
	}
	inputs := make([]memory.EmbeddingInput, 0, len(rows))
	for _, row := range rows {
		inputs = append(inputs, memory.EmbeddingInput{ID: row.ID, Title: row.Title, Summary: row.Summary})
	}
	return inputs, nil
}

// ExistingMemoryEmbeddingHashes 返回已入库向量的文本 hash，用于增量跳过。
func (r *MemoryRepo) ExistingMemoryEmbeddingHashes(ctx context.Context) (map[string]string, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID   string
		Hash string
	}
	err := r.db.WithContext(ctx).Table("memory_records").
		Select("id, embedding_text_hash AS hash").
		Where("embedding IS NOT NULL AND COALESCE(embedding_text_hash,'') <> ''").
		Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"查询记忆向量状态失败", err}
	}
	hashes := make(map[string]string, len(rows))
	for _, row := range rows {
		hashes[row.ID] = row.Hash
	}
	return hashes, nil
}

// UpsertMemoryEmbeddings 只更新既有记录的向量列，不新建记忆行。
func (r *MemoryRepo) UpsertMemoryEmbeddings(ctx context.Context, records []memory.EmbeddingRecord) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	if len(records) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, record := range records {
			if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.Hash) == "" {
				return store.ErrInvalid
			}
			literal, err := vectorLiteral(record.Embedding, memory.EmbeddingDimension)
			if err != nil {
				return err
			}
			if err := tx.Exec(`
UPDATE memory_records
SET embedding=?::vector, embedding_text_hash=?, embedding_updated_at=clock_timestamp()
WHERE id=?`, literal, record.Hash, record.ID).Error; err != nil {
				return &safeError{"写入记忆向量失败", err}
			}
		}
		return nil
	})
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
// SoulCandidates 选「够格进灵魂记忆」的记录：fact/preference 且 importance 达标。
//
// 灵魂记忆是公开长期设定：只取有效、未归档、公开来源的记录；
// 私密 DM 与其错误降密行不能进入。排序按重要度与新鲜度，供上层去重后截断。
func (r *MemoryRepo) SoulCandidates(ctx context.Context, minImportance float64, limit int) ([]store.MemoryRecord, error) {
	if ctx == nil || minImportance <= 0 || minImportance > 1 || limit < 1 || limit > 200 {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID         string
		Source     string
		Kind       string
		Title      string
		Summary    string
		Importance float64
		Confidence float64
		Private    bool
		Channel    event.Channel
		Session    string
		FromID     string
		FromName   string
		ValidFrom  time.Time
		CreatedAt  time.Time
		IDs        json.RawMessage
	}
	err := r.db.WithContext(ctx).Table("memory_records").
		Where("kind IN ? AND valid_to IS NULL AND NOT archived AND importance >= ?", []string{"fact", "preference"}, minImportance).
		Where("NOT private AND (channel IS NULL OR channel <> 'dm' OR private)").
		Select("id, source, kind, title, summary, importance, confidence, private, COALESCE(channel,'') AS channel, COALESCE(session,'') AS session, COALESCE(from_id,'') AS from_id, COALESCE(from_name,'') AS from_name, valid_from, created_at, to_json(event_ids) AS ids").
		Order("importance DESC, valid_from DESC, id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取灵魂记忆候选失败", err}
	}
	out := make([]store.MemoryRecord, 0, len(rows))
	for _, row := range rows {
		var ids []string
		if len(row.IDs) > 0 {
			_ = json.Unmarshal(row.IDs, &ids)
		}
		out = append(out, store.MemoryRecord{
			Record: memory.Record{
				ID: row.ID, Channel: row.Channel, Session: row.Session, Summary: row.Summary,
				Importance: row.Importance, Confidence: row.Confidence, Private: row.Private,
				EventIDs: ids, ValidFrom: row.ValidFrom,
			},
			Source: row.Source, Kind: row.Kind, Title: row.Title,
			FromID: row.FromID, FromName: row.FromName, CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// FactsByViewer 按观众 uid 查公开事实（认人用），新的在前。
//
// 认人是公开场景：只读公开来源，私密 DM 事实不能经 uid 被认出来。
func (r *MemoryRepo) FactsByViewer(ctx context.Context, fromID string, limit int) ([]store.MemoryRecord, error) {
	if ctx == nil || strings.TrimSpace(fromID) == "" || limit < 1 || limit > 50 {
		return nil, store.ErrInvalid
	}
	return r.queryFacts(ctx,
		"from_id = ? AND kind = 'fact' AND valid_to IS NULL AND NOT archived AND NOT private AND (channel IS NULL OR channel <> 'dm' OR private)",
		fromID, limit)
}

func (r *MemoryRepo) queryFacts(ctx context.Context, where string, arg any, limit int) ([]store.MemoryRecord, error) {
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
		FromID     string
		FromName   string
		ValidFrom  time.Time
		CreatedAt  time.Time
		IDs        json.RawMessage
	}
	err := r.db.WithContext(ctx).Table("memory_records").Where(where, arg).
		Select("id, source, kind, title, summary, body, importance, confidence, private, COALESCE(channel,'') AS channel, COALESCE(session,'') AS session, COALESCE(from_id,'') AS from_id, COALESCE(from_name,'') AS from_name, valid_from, created_at, to_json(event_ids) AS ids").
		Order("valid_from DESC, id DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取事实失败", err}
	}
	out := make([]store.MemoryRecord, 0, len(rows))
	for _, row := range rows {
		var ids []string
		if len(row.IDs) > 0 {
			_ = json.Unmarshal(row.IDs, &ids)
		}
		out = append(out, store.MemoryRecord{
			Record: memory.Record{
				ID: row.ID, Channel: row.Channel, Session: row.Session, Summary: row.Summary,
				Importance: row.Importance, Confidence: row.Confidence, Private: row.Private,
				EventIDs: ids, ValidFrom: row.ValidFrom,
			},
			Source: row.Source, Kind: row.Kind, Title: row.Title, Body: row.Body,
			FromID: row.FromID, FromName: row.FromName, CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

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
