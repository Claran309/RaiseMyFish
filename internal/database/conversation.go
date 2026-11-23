package database

import (
	"context"
	"errors"
	"strings"
	"time"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// ConversationRepo 实现会话状态读写。
type ConversationRepo struct{ db *gorm.DB }

func NewConversationRepo(db *gorm.DB) (*ConversationRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &ConversationRepo{db: db}, nil
}

var _ store.ConversationRepo = (*ConversationRepo)(nil)

// LoadState 只返回未过期会话；过期行仍保留在库中供审计。
func (r *ConversationRepo) LoadState(ctx context.Context, id string, now time.Time) (store.ConversationState, bool, error) {
	if ctx == nil || strings.TrimSpace(id) == "" || now.IsZero() {
		return store.ConversationState{}, false, store.ErrInvalid
	}
	var row struct {
		ConversationID string
		Channel        string
		AccountID      string
		UserID         string
		TopicID        string
		Summary        string
		Unanswered     string
		TurnCount      int
		LastEventID    *string
		LastTurnAt     *time.Time
		ExpiresAt      *time.Time
	}
	err := r.db.WithContext(ctx).Table("conversation_state").Where("conversation_id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.ConversationState{}, false, nil
	}
	if err != nil {
		return store.ConversationState{}, false, &safeError{"读取会话状态失败", err}
	}
	if row.ExpiresAt != nil && !row.ExpiresAt.After(now) {
		return store.ConversationState{}, false, nil
	}
	state := store.ConversationState{
		ConversationID: row.ConversationID, Channel: row.Channel, AccountID: row.AccountID,
		UserID: row.UserID, TopicID: row.TopicID, Summary: row.Summary, Unanswered: row.Unanswered,
		TurnCount: row.TurnCount,
	}
	if row.LastEventID != nil {
		state.LastEventID = *row.LastEventID
	}
	if row.LastTurnAt != nil {
		state.LastTurnAt = *row.LastTurnAt
	}
	if row.ExpiresAt != nil {
		state.ExpiresAt = *row.ExpiresAt
	}
	return state, true, nil
}

// Touch 幂等更新会话：轮数只增，摘要非空才覆盖，过期时间跟随最后一次发言。
func (r *ConversationRepo) Touch(ctx context.Context, state store.ConversationState) error {
	if ctx == nil || strings.TrimSpace(state.ConversationID) == "" || strings.TrimSpace(state.Channel) == "" {
		return store.ErrInvalid
	}
	err := r.db.WithContext(ctx).Exec(`INSERT INTO conversation_state
(conversation_id,channel,account_id,user_id,topic_id,summary,unanswered,turn_count,last_event_id,last_turn_at,expires_at)
VALUES(?,?,?,?,?,?,?,?,NULLIF(?,''),?,?)
ON CONFLICT (conversation_id) DO UPDATE SET
  summary = CASE WHEN EXCLUDED.summary <> '' THEN EXCLUDED.summary ELSE conversation_state.summary END,
  unanswered = EXCLUDED.unanswered,
  turn_count = GREATEST(conversation_state.turn_count, EXCLUDED.turn_count),
  last_event_id = COALESCE(EXCLUDED.last_event_id, conversation_state.last_event_id),
  last_turn_at = GREATEST(COALESCE(conversation_state.last_turn_at, EXCLUDED.last_turn_at), EXCLUDED.last_turn_at),
  expires_at = EXCLUDED.expires_at,
  updated_at = now()`,
		state.ConversationID, state.Channel, state.AccountID, state.UserID, state.TopicID,
		state.Summary, state.Unanswered, state.TurnCount, state.LastEventID, state.LastTurnAt, state.ExpiresAt).Error
	if err != nil {
		return &safeError{"更新会话状态失败", err}
	}
	return nil
}

// RecentTurns 取同一会话最近 N 条入向/出向消息，时间升序返回。
func (r *ConversationRepo) RecentTurns(ctx context.Context, id string, limit int) ([]store.ConversationTurn, error) {
	if ctx == nil || strings.TrimSpace(id) == "" || limit < 1 || limit > 100 {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID       string
		FromID   string
		FromName string
		Dir      string
		Text     string
		At       time.Time
	}
	err := r.db.WithContext(ctx).Raw(`
SELECT id,from_id,from_name,dir,text,at FROM (
  SELECT id,from_id,from_name,dir,text,at
    FROM events
   WHERE conversation_id=? AND (dir <> 'out' OR status='done')
   ORDER BY at DESC, id DESC
   LIMIT ?
) recent ORDER BY at ASC, id ASC`, id, limit).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"读取会话轮次失败", err}
	}
	out := make([]store.ConversationTurn, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.ConversationTurn{EventID: row.ID, FromID: row.FromID, FromName: row.FromName, Dir: row.Dir, Text: row.Text, At: row.At})
	}
	return out, nil
}
