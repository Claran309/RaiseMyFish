package database

import (
	"RaiseMyFish/internal/store"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"time"
)

// UsageRepo 只写既有llm_usage，不创建或更改schema。
type UsageRepo struct{ db *gorm.DB }

// NewUsageRepo 注入由infra管理的连接。
func NewUsageRepo(db *gorm.DB) (*UsageRepo, error) {
	if db == nil {
		return nil, errors.New("用量repository需要数据库连接")
	}
	return &UsageRepo{db: db}, nil
}

type usageRow struct {
	At           time.Time `gorm:"column:at"`
	Channel      string    `gorm:"column:channel"`
	Model        string    `gorm:"column:model"`
	InputTokens  *int      `gorm:"column:in_tokens"`
	OutputTokens *int      `gorm:"column:out_tokens"`
	CostUSD      *float64  `gorm:"column:cost_usd"`
	Milliseconds int       `gorm:"column:ms"`
	OK           bool      `gorm:"column:ok"`
}

// RecordUsage 使用参数化GORM写入；每次实际尝试一行，未知字段保留NULL。
func (r *UsageRepo) RecordUsage(ctx context.Context, u store.Usage) error {
	if ctx == nil {
		return errors.New("记录用量需要Context")
	}
	if err := u.Validate(); err != nil {
		return fmt.Errorf("拒绝无效LLM用量：%w", err)
	}
	row := usageRow{At: u.At, Channel: string(u.Channel), Model: u.Model, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CostUSD: u.CostUSD, Milliseconds: u.Milliseconds, OK: u.OK}
	if err := r.db.WithContext(ctx).Table("llm_usage").Create(&row).Error; err != nil {
		return &safeError{"记录LLM用量失败", err}
	}
	return nil
}
