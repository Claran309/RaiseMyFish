package database

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"time"
)

// MemoryRepo 执行范围受限的持久化读写，不实现提炼/Reader编排。
type MemoryRepo struct{ db *gorm.DB }

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
	query := r.db.WithContext(ctx).Table("events").Where("channel=? AND session=?", s.Channel, s.Session)
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
	query := r.db.WithContext(ctx).Table("events").Where("channel <> ? AND channel <> 'dm' AND NOT private", s.Channel)
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
