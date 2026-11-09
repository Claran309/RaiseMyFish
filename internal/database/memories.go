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
	"unicode"
	"unicode/utf8"
)

const (
	// memoryRecordLimitMax 是运维查询的单次上限，避免误把整表读进内存。
	memoryRecordLimitMax = 200
	// memorySearchMaxRunes 限制搜索词长度，防止构造超长 SQL 参数。
	memorySearchMaxRunes = 200
	// defaultRecordSubjectQuota 是同一主体一次召回最多占用的摘要条数。
	// 高重要度的固定主题不能靠重要度长期占满窗口，普通熟人才有机会出现。
	defaultRecordSubjectQuota = 3
	// recordQueryTermLimit 限制一次相关性匹配使用的检索词数量。
	recordQueryTermLimit = 8
	// focusSpeakerMaxRunes 限制焦点查询的说话者标识长度，防止构造超长参数。
	focusSpeakerMaxRunes = 128
)

// MemoryRepo 执行范围受限的持久化读写，不实现提炼/Reader编排。
type MemoryRepo struct{ db *gorm.DB }

// 编译期断言：事实查询同时满足 store 与 memory 两侧的窄接口。
var (
	_ store.MemoryRepo         = (*MemoryRepo)(nil)
	_ store.FactSource         = (*MemoryRepo)(nil)
	_ memory.FactSource        = (*MemoryRepo)(nil)
	_ memory.RecordSource      = (*MemoryRepo)(nil)
	_ memory.RecordQuerySource = (*MemoryRepo)(nil)
	_ memory.FocusSource       = (*MemoryRepo)(nil)
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
	return insertMemoryRecord(ctx, r.db, m)
}

// insertMemoryRecord 是 InsertRecord 与「提炼结果 + 任务完成同事务」共用的写入实现。
//
// db 可以是普通连接，也可以是事务；事务边界（先租约栅栏、后写入、再提交）
// 由调用方负责，本函数只保证单条记录的 SQL 语义一致。
func insertMemoryRecord(ctx context.Context, db *gorm.DB, m store.MemoryRecord) (bool, error) {
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
	result := db.WithContext(ctx).Exec(`INSERT INTO memory_records
(id,source,kind,title,summary,body,importance,confidence,private,channel,session,valid_from,event_ids,from_id,from_name,asserted_by_id,asserted_by_name,subject)
VALUES (?,?,?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,ARRAY(SELECT jsonb_array_elements_text(NULLIF(?::jsonb,'null'::jsonb))),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''))
ON CONFLICT (id) DO UPDATE SET
  summary    = EXCLUDED.summary,
  confidence = EXCLUDED.confidence,
  valid_from = EXCLUDED.valid_from,
  event_ids  = EXCLUDED.event_ids,
  from_id    = COALESCE(EXCLUDED.from_id, memory_records.from_id),
  from_name  = COALESCE(EXCLUDED.from_name, memory_records.from_name),
  asserted_by_id   = COALESCE(EXCLUDED.asserted_by_id, memory_records.asserted_by_id),
  asserted_by_name = COALESCE(EXCLUDED.asserted_by_name, memory_records.asserted_by_name),
  subject    = COALESCE(EXCLUDED.subject, memory_records.subject)
WHERE memory_records.kind = 'fact'
  AND EXCLUDED.kind = 'fact'
  AND memory_records.private = EXCLUDED.private
  AND COALESCE(memory_records.channel,'') = COALESCE(EXCLUDED.channel,'')
  AND COALESCE(memory_records.session,'') = COALESCE(EXCLUDED.session,'')
  AND EXCLUDED.valid_from > memory_records.valid_from
  AND memory_records.summary IS DISTINCT FROM EXCLUDED.summary`, m.ID, m.Source, m.Kind, m.Title, m.Summary, m.Body, m.Importance, m.Confidence, m.Private, m.Channel, m.Session, m.ValidFrom, string(ids), m.FromID, m.FromName, m.AssertedByID, m.AssertedByName, m.Subject)
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

// FocusMessages 取当前说话者的双方对话轮次，按 reply_to 把鱼的 out 回复配到入向问题上。
//
// 窗口与 Recent 完全独立：房间刷屏只影响现场流，不会把本轮对话挤出去。
// 可见性口径与 Recent 一致（会话隔离、私密过滤、DM 不外泄），只返回已确认的 out。
func (r *MemoryRepo) FocusMessages(ctx context.Context, q memory.FocusQuery) ([]memory.Message, error) {
	if err := validateRecall(ctx, q.Scope, q.Limit); err != nil {
		return nil, err
	}
	speaker := strings.TrimSpace(q.Speaker)
	if speaker == "" || utf8.RuneCountInString(speaker) > focusSpeakerMaxRunes {
		return nil, store.ErrInvalid
	}
	// 一半配额给入向问题，另一半留给配对回复；成对返回才不会只有问题没有回答。
	inLimit := (q.Limit + 1) / 2
	var messages []memory.Message
	err := r.db.WithContext(ctx).Raw(`
WITH focus_in AS (
    SELECT id AS event_id,channel,session,dir,from_id,from_name,text,reply_to,private,at
    FROM events
    WHERE channel=? AND session=? AND dir='in' AND from_id=? AND `+eventVisibilitySQL(q.Scope, "")+`
    ORDER BY at DESC, id DESC
    LIMIT ?
), focus_out AS (
    SELECT o.id AS event_id,o.channel,o.session,o.dir,o.from_id,o.from_name,o.text,o.reply_to,o.private,o.at
    FROM events o
    JOIN focus_in i ON o.reply_to = i.event_id
    WHERE o.dir='out' AND o.status='done' AND o.channel=? AND o.session=? AND `+eventVisibilitySQL(q.Scope, "o")+`
)
SELECT event_id,channel,session,dir,from_id,from_name,text,reply_to,private,at
FROM (
    SELECT * FROM (SELECT * FROM focus_in UNION ALL SELECT * FROM focus_out) merged
    ORDER BY at DESC, event_id DESC
    LIMIT ?
) latest
ORDER BY at ASC, event_id ASC`, q.Scope.Channel, q.Scope.Session, speaker, inLimit, q.Scope.Channel, q.Scope.Session, q.Limit).Scan(&messages).Error
	if err != nil {
		return nil, &safeError{"查询焦点对话失败", err}
	}
	if err := (memory.Context{Scope: q.Scope, Focus: messages}).Validate(); err != nil {
		return nil, &safeError{"焦点对话存量数据无效", err}
	}
	return messages, nil
}

// eventVisibilitySQL 生成事件可见性条件；alias 用于 JOIN 中的另一张 events 表。
func eventVisibilitySQL(s memory.Scope, alias string) string {
	column := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	parts := make([]string, 0, 2)
	if !s.Private {
		parts = append(parts, "NOT "+column("private"))
	}
	parts = append(parts, "("+column("channel")+" <> 'dm' OR "+column("private")+")")
	return strings.Join(parts, " AND ")
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
//
// 先在每个主体内限额，再按重要度取前 limit 条：同一 from_id/主题最多
// defaultRecordSubjectQuota 条，避免少数固定主题长期占满上下文。
func (r *MemoryRepo) Records(ctx context.Context, s memory.Scope, limit int) ([]memory.Record, error) {
	return r.records(ctx, s, "", limit)
}

// RecordsFor 在 Records 的基础上优先返回与 query 相关的摘要。
//
// query 只影响排序与主体内的取舍，不改变可见范围；空查询等同 Records。
func (r *MemoryRepo) RecordsFor(ctx context.Context, s memory.Scope, query string, limit int) ([]memory.Record, error) {
	return r.records(ctx, s, query, limit)
}

// records 是 Records/RecordsFor 的共享实现。
//
// 主体优先取 from_id（同一观众），没有主体时才退回 title（同一主题）；
// 两者都缺时用自身 ID 自成一组，避免把无关记录误合并。
func (r *MemoryRepo) records(ctx context.Context, s memory.Scope, query string, limit int) ([]memory.Record, error) {
	if err := validateRecall(ctx, s, limit); err != nil {
		return nil, err
	}

	terms := recordQueryTerms(query)
	relevance := "FALSE"
	args := make([]any, 0, len(terms)*2+4)
	if len(terms) > 0 {
		clauses := make([]string, 0, len(terms))
		for _, term := range terms {
			pattern := "%" + escapeLikePattern(term) + "%"
			clauses = append(clauses, `(title ILIKE ? ESCAPE '\' OR summary ILIKE ? ESCAPE '\')`)
			args = append(args, pattern, pattern)
		}
		relevance = "(" + strings.Join(clauses, " OR ") + ")"
	}
	visibility, visArgs := memoryVisibilitySQL(s)
	args = append(args, visArgs...)
	args = append(args, defaultRecordSubjectQuota, limit)

	var rows []memoryRecordRow
	err := r.db.WithContext(ctx).Raw(`
WITH candidates AS (
    SELECT id, COALESCE(channel,'') AS channel, COALESCE(session,'') AS session,
           summary, importance, confidence, private, valid_from,
           to_json(event_ids) AS ids,
           CASE WHEN `+relevance+` THEN 1 ELSE 0 END AS relevant,
           CASE
             WHEN COALESCE(btrim(from_id), '') <> '' THEN 'uid:' || btrim(from_id)
             WHEN COALESCE(btrim(title), '') <> '' THEN 'title:' || lower(btrim(title))
             ELSE 'id:' || id
           END AS subject
    FROM memory_records
    WHERE valid_to IS NULL AND NOT archived AND valid_from <= now()
      AND `+visibility+`
),
ranked AS (
    SELECT candidates.*,
           ROW_NUMBER() OVER (
               PARTITION BY subject
               ORDER BY relevant DESC, importance DESC, valid_from DESC, id ASC
           ) AS subject_rank
    FROM candidates
)
SELECT id, channel, session, summary, importance, confidence, private, valid_from, ids
FROM ranked
WHERE subject_rank <= ?
ORDER BY relevant DESC, importance DESC, id ASC
LIMIT ?`, args...).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"查询记忆摘要失败", err}
	}
	records, err := memoryRows(rows)
	if err != nil {
		return nil, err
	}
	if err := (memory.Context{Scope: s, Records: records}).Validate(); err != nil {
		return nil, &safeError{"记忆摘要存量数据无效", err}
	}
	return records, nil
}

// recordQueryTerms 把问题文本拆成 title/summary 匹配词。
//
// 整句优先（短查询直接命中），再按空白与常见标点切分；
// 单字词不参与，避免「的/是」这类字把无关摘要抬到前面。
func recordQueryTerms(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	if runes := []rune(query); len(runes) > memorySearchMaxRunes {
		query = string(runes[:memorySearchMaxRunes])
	}
	seen := make(map[string]bool, recordQueryTermLimit)
	terms := make([]string, 0, recordQueryTermLimit)
	add := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" || utf8.RuneCountInString(term) < 2 || seen[term] {
			return
		}
		seen[term] = true
		terms = append(terms, term)
	}
	add(query)
	for _, term := range strings.FieldsFunc(query, isRecordQuerySeparator) {
		if len(terms) >= recordQueryTermLimit {
			break
		}
		add(term)
	}
	return terms
}

// isRecordQuerySeparator 是问句里不承载主题的切分符。
func isRecordQuerySeparator(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("，。！？、；：,.!?;:（）()[]【】…—－-“”\"'", r)
}

// escapeLikePattern 转义 LIKE 通配符，让查询词按字面匹配。
func escapeLikePattern(term string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term)
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
//
// stale 拒绝（C2/N08）：向量非空还不够——
//   - 索引指纹必须等于查询侧指纹（换模型/提供方/维度/模板后旧向量不参与排序）；
//   - 写回时的源 hash 必须等于当前源字段 hash（summary 更新后旧向量立即失效）；
//   - 索引文本 hash 非空（未标记索引版本的向量不可信）。
//
// 三者任一不满足都不进入排序，宁可暂时少召回，也不拿旧语义给新正文排序。
func (r *MemoryRepo) SemanticRecords(ctx context.Context, s memory.Scope, vector []float64, fingerprint string, limit int) ([]memory.SemanticRecord, error) {
	if err := validateRecall(ctx, s, limit); err != nil {
		return nil, err
	}
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return nil, store.ErrInvalid
	}
	literal, err := vectorLiteral(vector, memory.EmbeddingDimension)
	if err != nil {
		return nil, store.ErrInvalid
	}
	visibility, args := memoryVisibilitySQL(s)
	params := make([]any, 0, len(args)+3)
	params = append(params, literal, fingerprint)
	params = append(params, args...)
	params = append(params, limit)
	var rows []memoryRecordRow
	err = r.db.WithContext(ctx).Raw(`
WITH q AS (SELECT ?::vector AS v)
SELECT id, channel, session, summary, importance, confidence, private, valid_from,
       to_json(event_ids) AS ids, 1 - (embedding <=> q.v) AS sim
FROM memory_records, q
WHERE valid_to IS NULL AND NOT archived AND valid_from <= now()
  AND embedding IS NOT NULL
  AND COALESCE(embedding_text_hash,'') <> ''
  AND embedding_fingerprint = ?
  AND embedding_source_hash = `+memorySourceHashExpr+`
  AND `+visibility+`
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

// MemoryEmbeddingInputs 返回允许外发的向量化源字段（增量候选）。
//
// 选择策略在 SQL 层先执行一遍：只取当前有效（valid_to IS NULL、未归档、
// valid_from 已生效）且摘要非空的行；私密记录只有在 allowPrivate 显式授权时
// 才离开数据库。领域层 RunEmbedding 会按同样规则再校验一次。
func (r *MemoryRepo) MemoryEmbeddingInputs(ctx context.Context, allowPrivate bool) ([]memory.EmbeddingInput, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID         string
		Title      string
		Summary    string
		SourceHash string
		Private    bool
		Archived   bool
		ValidFrom  time.Time
		ValidTo    *time.Time
	}
	query := r.db.WithContext(ctx).Table("memory_records").
		Select("id, COALESCE(title,'') AS title, COALESCE(summary,'') AS summary, " +
			memorySourceHashExpr + " AS source_hash, private, archived, valid_from, valid_to").
		Where("COALESCE(summary,'') <> '' AND valid_to IS NULL AND NOT archived AND valid_from <= now()").
		Order("id ASC")
	if !allowPrivate {
		query = query.Where("NOT private")
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, &safeError{"读取记忆向量化源失败", err}
	}
	inputs := make([]memory.EmbeddingInput, 0, len(rows))
	for _, row := range rows {
		inputs = append(inputs, memory.EmbeddingInput{
			ID:         row.ID,
			Title:      row.Title,
			Summary:    row.Summary,
			SourceHash: row.SourceHash,
			Private:    row.Private,
			Archived:   row.Archived,
			ValidFrom:  row.ValidFrom,
			ValidTo:    row.ValidTo,
		})
	}
	return inputs, nil
}

// ExistingMemoryEmbeddingIndexes 返回已入库向量的索引状态，用于增量跳过。
//
// 三个 hash 各司其职：文本 hash 判断正文是否变化，源 hash 判断写回后源是否变化，
// 指纹判断向量空间是否一致。只要任一不一致都必须重新向量化。
func (r *MemoryRepo) ExistingMemoryEmbeddingIndexes(ctx context.Context) (map[string]memory.EmbeddingIndex, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID          string
		TextHash    string
		SourceHash  string
		Fingerprint string
	}
	err := r.db.WithContext(ctx).Table("memory_records").
		Select("id, COALESCE(embedding_text_hash,'') AS text_hash, COALESCE(embedding_source_hash,'') AS source_hash, COALESCE(embedding_fingerprint,'') AS fingerprint").
		Where("embedding IS NOT NULL").
		Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"查询记忆向量状态失败", err}
	}
	indexes := make(map[string]memory.EmbeddingIndex, len(rows))
	for _, row := range rows {
		indexes[row.ID] = memory.EmbeddingIndex{
			TextHash:    row.TextHash,
			SourceHash:  row.SourceHash,
			Fingerprint: row.Fingerprint,
		}
	}
	return indexes, nil
}

// UpsertMemoryEmbeddings 只更新既有记录的向量列，不新建记忆行。
//
// 写回用 CAS 校验源未变化：源 hash 在读取时随输入下发，写回时再与当前行
// 重新计算的源 hash 比较；不一致说明读取后摘要已被更新或清洗，本次结果
// 属于过期计算，必须拒绝写回，否则会把刚清掉的污染向量重新种回去。
// 返回实际写入条数，被拒绝的由调用方计入 Rejected。
func (r *MemoryRepo) UpsertMemoryEmbeddings(ctx context.Context, records []memory.EmbeddingRecord) (int, error) {
	if ctx == nil {
		return 0, store.ErrInvalid
	}
	if len(records) == 0 {
		return 0, nil
	}
	written := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, record := range records {
			if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.Hash) == "" ||
				strings.TrimSpace(record.SourceHash) == "" || strings.TrimSpace(record.Fingerprint) == "" {
				return store.ErrInvalid
			}
			literal, err := vectorLiteral(record.Embedding, memory.EmbeddingDimension)
			if err != nil {
				return err
			}
			result := tx.Exec(`
UPDATE memory_records
SET embedding=?::vector, embedding_text_hash=?, embedding_source_hash=?, embedding_fingerprint=?, embedding_updated_at=clock_timestamp()
WHERE id=? AND `+memorySourceHashExpr+` = ?`,
				literal, record.Hash, record.SourceHash, record.Fingerprint, record.ID, record.SourceHash)
			if result.Error != nil {
				return &safeError{"写入记忆向量失败", result.Error}
			}
			if result.RowsAffected == 1 {
				written++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
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
		ID             string
		Source         string
		Kind           string
		Title          string
		Summary        string
		Body           string
		Importance     float64
		Confidence     float64
		Private        bool
		Channel        event.Channel
		Session        string
		FromID         string
		FromName       string
		AssertedByID   string
		AssertedByName string
		Subject        string
		ValidFrom      time.Time
		CreatedAt      time.Time
		IDs            json.RawMessage
	}
	err := query.
		Select("id, source, kind, title, summary, body, importance, confidence, private, COALESCE(channel,'') AS channel, COALESCE(session,'') AS session, COALESCE(from_id,'') AS from_id, COALESCE(from_name,'') AS from_name, COALESCE(asserted_by_id,'') AS asserted_by_id, COALESCE(asserted_by_name,'') AS asserted_by_name, COALESCE(subject,'') AS subject, valid_from, created_at, to_json(event_ids) AS ids").
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
			Source:         row.Source,
			Kind:           row.Kind,
			Title:          row.Title,
			Body:           row.Body,
			FromID:         row.FromID,
			FromName:       row.FromName,
			AssertedByID:   row.AssertedByID,
			AssertedByName: row.AssertedByName,
			Subject:        row.Subject,
			CreatedAt:      row.CreatedAt,
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
// 灵魂记忆是公开长期设定：只取当前有效、未归档、公开来源的记录；
// 私密 DM 与其错误降密行不能进入。未来才生效（valid_from > now()）的记录
// 同样不能提前进入，避免历史回放/预写事实污染当前设定。
// 排序按重要度与新鲜度，供上层去重后截断。
func (r *MemoryRepo) SoulCandidates(ctx context.Context, minImportance float64, limit int) ([]store.MemoryRecord, error) {
	if ctx == nil || minImportance <= 0 || minImportance > 1 || limit < 1 || limit > 200 {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID             string
		Source         string
		Kind           string
		Title          string
		Summary        string
		Importance     float64
		Confidence     float64
		Private        bool
		Channel        event.Channel
		Session        string
		FromID         string
		FromName       string
		AssertedByID   string
		AssertedByName string
		Subject        string
		ValidFrom      time.Time
		CreatedAt      time.Time
		IDs            json.RawMessage
	}
	err := r.db.WithContext(ctx).Table("memory_records").
		Where("kind IN ? AND valid_to IS NULL AND NOT archived AND valid_from <= now() AND importance >= ?", []string{"fact", "preference"}, minImportance).
		Where("NOT private AND (channel IS NULL OR channel <> 'dm' OR private)").
		Select("id, source, kind, title, summary, importance, confidence, private, COALESCE(channel,'') AS channel, COALESCE(session,'') AS session, COALESCE(from_id,'') AS from_id, COALESCE(from_name,'') AS from_name, COALESCE(asserted_by_id,'') AS asserted_by_id, COALESCE(asserted_by_name,'') AS asserted_by_name, COALESCE(subject,'') AS subject, valid_from, created_at, to_json(event_ids) AS ids").
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
			FromID: row.FromID, FromName: row.FromName,
			AssertedByID: row.AssertedByID, AssertedByName: row.AssertedByName,
			Subject:   row.Subject,
			CreatedAt: row.CreatedAt,
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
	err := query.Select("id AS event_id,channel,session,dir,from_id,from_name,text,reply_to,private,at").Order("at DESC, id DESC").Limit(limit).Find(&messages).Error
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
