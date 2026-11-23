package store

import (
	"context"
	"time"
)

// ConversationState 是会话的摘要与最近轮次元数据；不存正文。
type ConversationState struct {
	ConversationID string
	Channel        string
	AccountID      string
	UserID         string
	TopicID        string
	Summary        string
	Unanswered     string
	TurnCount      int
	LastEventID    string
	LastTurnAt     time.Time
	ExpiresAt      time.Time
}

// ConversationTurn 是会话里的一轮消息（只读投影）。
type ConversationTurn struct {
	EventID  string
	FromID   string
	FromName string
	Dir      string
	Text     string
	At       time.Time
}

// ConversationRepo 读写会话状态与最近轮次；隐私过滤在 SQL 层。
type ConversationRepo interface {
	LoadState(context.Context, string, time.Time) (ConversationState, bool, error)
	Touch(context.Context, ConversationState) error
	RecentTurns(context.Context, string, int) ([]ConversationTurn, error)
}
