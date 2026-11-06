package database

// S04：评论/私信通道的不可变投递计划与尝试。
//
// 与 live 的 SendRepo 同一套「先计划、再 arm、最后记录结果」纪律，但不复用其
// 平台专属的许可/核验语义：这些通道没有幂等键，arm 之后崩溃只能按未知处理。

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

const (
	// maxPlanAttempts 与 live 一致：可信未投递最多重试 3 次。
	maxPlanAttempts = 3
	// planPermitTTL 只是本地许可的审计窗口，不参与平台请求。
	planPermitTTL = time.Minute
)

// PlannedRepo 实现 store.PlannedRepo。
type PlannedRepo struct {
	db *gorm.DB
}

var _ store.PlannedRepo = (*PlannedRepo)(nil)

func NewPlannedRepo(db *gorm.DB) (*PlannedRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &PlannedRepo{db: db}, nil
}

type deliveryPlanRow struct {
	EventID          string `gorm:"column:event_id"`
	Channel          string
	AccountID        string `gorm:"column:account_id"`
	Destination      string
	Text             string
	TextSHA          string `gorm:"column:text_sha"`
	State            string
	CurrentAttemptID string    `gorm:"column:current_attempt_id"`
	AttemptCount     int       `gorm:"column:attempt_count"`
	CreatedAt        time.Time `gorm:"column:created_at"`
}

type deliveryAttemptRow struct {
	ID          string
	EventID     string `gorm:"column:event_id"`
	Channel     string
	AccountID   string `gorm:"column:account_id"`
	Destination string
	TextSHA     string `gorm:"column:text_sha"`
	Number      int
	State       string
	Nonce       string
	ArmedAt     *time.Time `gorm:"column:armed_at"`
	PermitUntil *time.Time `gorm:"column:permit_until"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	OK          *bool
	Verified    bool
	MsgID       string `gorm:"column:msg_id"`
	ErrorKind   string `gorm:"column:error_kind"`
}

func (p deliveryPlanRow) attempt(a deliveryAttemptRow) sender.Attempt {
	return sender.Attempt{
		ID:          a.ID,
		EventID:     p.EventID,
		ActorID:     p.AccountID,
		RoomID:      p.Destination,
		Text:        p.Text,
		Nonce:       a.Nonce,
		State:       a.State,
		ErrorKind:   a.ErrorKind,
		MsgID:       a.MsgID,
		Number:      a.Number,
		Verified:    a.Verified,
		CreatedAt:   a.CreatedAt,
		PermitUntil: a.PermitUntil,
	}
}

func validPlanKey(key store.PlanKey) bool {
	if key.Channel != event.ChComment && key.Channel != event.ChDM {
		return false
	}
	return sender.ValidActor(key.AccountID) && sender.ValidActor(key.Destination)
}

func validPlanText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return false
	}
	return utf8.RuneCountInString(text) <= 4000
}

func planActual(row queueRow, key store.PlanKey) error {
	e := row.Event
	if e.Channel != key.Channel || e.Dir != event.DirIn || e.Session != key.Destination {
		return store.ErrInvalid
	}
	if e.Private != (key.Channel == event.ChDM) {
		return store.ErrInvalid
	}
	return nil
}

func planMatches(p deliveryPlanRow, key store.PlanKey, eventID string) error {
	if p.EventID != eventID || p.Channel != string(key.Channel) || p.AccountID != key.AccountID || p.Destination != key.Destination {
		return store.ErrConflict
	}
	return nil
}

func planCurrentAttempt(tx *gorm.DB, p deliveryPlanRow) (deliveryAttemptRow, error) {
	var a deliveryAttemptRow
	err := tx.Table("delivery_attempts").Where("id=? AND event_id=?", p.CurrentAttemptID, p.EventID).Take(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return a, store.ErrConflict
	}
	if err != nil {
		return a, err
	}
	if a.Channel != p.Channel || a.AccountID != p.AccountID || a.Destination != p.Destination || a.TextSHA != p.TextSHA {
		return a, store.ErrConflict
	}
	return a, nil
}

func planNewAttempt(tx *gorm.DB, p *deliveryPlanRow) (deliveryAttemptRow, error) {
	number := p.AttemptCount + 1
	if number > maxPlanAttempts {
		return deliveryAttemptRow{}, store.ErrConflict
	}
	id, err := sendUUID()
	if err != nil {
		return deliveryAttemptRow{}, err
	}
	nonce, err := sendUUID()
	if err != nil {
		return deliveryAttemptRow{}, err
	}
	a := deliveryAttemptRow{
		ID: id, EventID: p.EventID, Channel: p.Channel, AccountID: p.AccountID,
		Destination: p.Destination, TextSHA: p.TextSHA, Number: number,
		State: sender.StateReady, Nonce: nonce,
	}
	if err := tx.Table("delivery_attempts").Create(&a).Error; err != nil {
		return deliveryAttemptRow{}, err
	}
	if err := tx.Table("delivery_plans").Where("event_id=?", p.EventID).Updates(map[string]any{
		"current_attempt_id": a.ID,
		"attempt_count":      number,
		"state":              sender.StateReady,
	}).Error; err != nil {
		return deliveryAttemptRow{}, err
	}
	p.CurrentAttemptID = a.ID
	p.AttemptCount = number
	p.State = sender.StateReady
	return a, nil
}

// PreparePlan 在首次 POST 前写入不可变正文；已存在计划时只返回原快照。
//
// 阶段保持在 generate：投递成功后由 Generate 的 Mark 负责 sent/verify，
// 这样「记录 attempt → 标 sent」之间仍保留可核验的恢复点。
func (r *PlannedRepo) PreparePlan(ctx context.Context, c event.Claim, key store.PlanKey, text string) (sender.Attempt, error) {
	if ctx == nil || !validClaim(c) || !validPlanKey(key) || !validPlanText(text) {
		return sender.Attempt{}, store.ErrInvalid
	}
	var out sender.Attempt
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if err := planActual(row, key); err != nil {
			return err
		}
		if row.Stage != event.StageGenerate {
			return store.ErrConflict
		}
		switch row.Event.Status {
		case event.StatusQueued, event.StatusReplying, event.StatusSent:
		default:
			return store.ErrConflict
		}
		var p deliveryPlanRow
		err = tx.Table("delivery_plans").Where("event_id=?", row.Event.ID).Take(&p).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			id, err := sendUUID()
			if err != nil {
				return err
			}
			nonce, err := sendUUID()
			if err != nil {
				return err
			}
			hash := sha256.Sum256([]byte(text))
			p = deliveryPlanRow{
				EventID: row.Event.ID, Channel: string(key.Channel), AccountID: key.AccountID,
				Destination: key.Destination, Text: text, TextSHA: hex.EncodeToString(hash[:]),
				State: sender.StateReady, CurrentAttemptID: id, AttemptCount: 1,
			}
			if err := tx.Table("delivery_plans").Create(&p).Error; err != nil {
				return err
			}
			a := deliveryAttemptRow{
				ID: id, EventID: p.EventID, Channel: p.Channel, AccountID: p.AccountID,
				Destination: p.Destination, TextSHA: p.TextSHA, Number: 1,
				State: sender.StateReady, Nonce: nonce,
			}
			if err := tx.Table("delivery_attempts").Create(&a).Error; err != nil {
				return err
			}
			out = p.attempt(a)
			return nil
		}
		if err != nil {
			return err
		}
		if err := planMatches(p, key, row.Event.ID); err != nil {
			return err
		}
		a, err := planCurrentAttempt(tx, p)
		if err != nil {
			return err
		}
		// 可信未投递（平台明确拒收/限流）回到 generate 重试时，换下一条尝试；
		// 正文仍沿用计划快照，不让模型重新生成。
		if a.State == sender.StateNotPosted && p.AttemptCount < maxPlanAttempts {
			a, err = planNewAttempt(tx, &p)
			if err != nil {
				return err
			}
		}
		out = p.attempt(a)
		return nil
	})
	return out, queueFailure("保存投递计划失败", err)
}

// LoadPlan 只读取已有计划；deliver 阶段的可信未投递会创建下一尝试。
func (r *PlannedRepo) LoadPlan(ctx context.Context, c event.Claim, key store.PlanKey) (sender.Attempt, error) {
	if ctx == nil || !validClaim(c) || !validPlanKey(key) {
		return sender.Attempt{}, store.ErrInvalid
	}
	var out sender.Attempt
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if err := planActual(row, key); err != nil {
			return err
		}
		if row.Stage != event.StageDeliver && row.Stage != event.StageVerify {
			return store.ErrConflict
		}
		var p deliveryPlanRow
		if err := tx.Table("delivery_plans").Where("event_id=?", row.Event.ID).Take(&p).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return store.ErrNotFound
			}
			return err
		}
		if err := planMatches(p, key, row.Event.ID); err != nil {
			return err
		}
		a, err := planCurrentAttempt(tx, p)
		if err != nil {
			return err
		}
		if row.Stage == event.StageDeliver && a.State == sender.StateNotPosted && p.AttemptCount < maxPlanAttempts {
			a, err = planNewAttempt(tx, &p)
			if err != nil {
				return err
			}
		}
		out = p.attempt(a)
		return nil
	})
	return out, queueFailure("读取投递计划失败", err)
}

// ArmPlan 是唯一允许进入平台调用的本地许可；提交成功后调用方才可 POST。
func (r *PlannedRepo) ArmPlan(ctx context.Context, c event.Claim, key store.PlanKey, attemptID string) (sender.Attempt, error) {
	if ctx == nil || !validClaim(c) || !validPlanKey(key) || strings.TrimSpace(attemptID) == "" {
		return sender.Attempt{}, store.ErrInvalid
	}
	var out sender.Attempt
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if err := planActual(row, key); err != nil {
			return err
		}
		if row.Stage != event.StageGenerate && row.Stage != event.StageDeliver {
			return store.ErrConflict
		}
		switch row.Event.Status {
		case event.StatusQueued, event.StatusReplying, event.StatusSent:
		default:
			return store.ErrConflict
		}
		var p deliveryPlanRow
		if err := tx.Table("delivery_plans").Where("event_id=?", row.Event.ID).Take(&p).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return store.ErrNotFound
			}
			return err
		}
		if err := planMatches(p, key, row.Event.ID); err != nil {
			return err
		}
		if p.CurrentAttemptID != attemptID {
			return store.ErrConflict
		}
		var a deliveryAttemptRow
		if err := tx.Table("delivery_attempts").Where("id=? AND event_id=?", attemptID, p.EventID).Take(&a).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return store.ErrConflict
			}
			return err
		}
		if a.State != sender.StateReady {
			return store.ErrConflict
		}
		if a.Channel != p.Channel || a.AccountID != p.AccountID || a.Destination != p.Destination || a.TextSHA != p.TextSHA {
			return store.ErrConflict
		}
		now, err := sendNow(tx)
		if err != nil {
			return err
		}
		until := now.Add(planPermitTTL)
		res := tx.Table("delivery_attempts").Where("id=? AND state=?", a.ID, sender.StateReady).Updates(map[string]any{
			"state": sender.StateArmed, "armed_at": now, "permit_until": until,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return store.ErrConflict
		}
		if err := tx.Table("delivery_plans").Where("event_id=? AND current_attempt_id=?", p.EventID, a.ID).Update("state", sender.StateArmed).Error; err != nil {
			return err
		}
		a.State, a.ArmedAt, a.PermitUntil = sender.StateArmed, &now, &until
		out = p.attempt(a)
		return nil
	})
	return out, queueFailure("授予投递许可失败", err)
}

// RecordPlan 记录一次平台调用结果；已知成功不会被后续结果覆盖。
func (r *PlannedRepo) RecordPlan(ctx context.Context, c event.Claim, key store.PlanKey, attemptID string, outcome store.PlanOutcome) (sender.Attempt, error) {
	if ctx == nil || !validClaim(c) || !validPlanKey(key) || strings.TrimSpace(attemptID) == "" || !validPlanOutcome(outcome) {
		return sender.Attempt{}, store.ErrInvalid
	}
	var out sender.Attempt
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 结果记录发生在 POST 之后，不能因为租约刚好过期就丢掉已知成功。
		// 这里不锁租约，只按事件身份与 attempt 归属做乐观校验。
		var row eventRow
		if err := tx.Table("events").Where("id=?", c.Event.ID).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return store.ErrNotFound
			}
			return err
		}
		if err := planActual(queueRow{Event: row}, key); err != nil {
			return err
		}
		var p deliveryPlanRow
		if err := tx.Table("delivery_plans").Where("event_id=?", row.ID).Take(&p).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return store.ErrNotFound
			}
			return err
		}
		if err := planMatches(p, key, row.ID); err != nil {
			return err
		}
		if p.CurrentAttemptID != attemptID {
			return store.ErrConflict
		}
		var a deliveryAttemptRow
		if err := tx.Table("delivery_attempts").Where("id=? AND event_id=?", attemptID, p.EventID).Take(&a).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return store.ErrConflict
			}
			return err
		}
		if a.State == sender.StateAccepted {
			out = p.attempt(a)
			return nil
		}
		if a.State == sender.StateReady {
			return store.ErrConflict
		}
		if a.Channel != p.Channel || a.AccountID != p.AccountID || a.Destination != p.Destination || a.TextSHA != p.TextSHA {
			return store.ErrConflict
		}
		switch a.State {
		case sender.StateDead, sender.StateSkipped, sender.StateManual:
			out = p.attempt(a)
			return nil
		}
		var ok *bool
		value := outcome.OK
		ok = &value
		res := tx.Table("delivery_attempts").Where("id=? AND state IN ?", a.ID, []string{sender.StateArmed, sender.StateUnknown}).Updates(map[string]any{
			"state": outcome.State, "ok": ok, "verified": outcome.Verified, "msg_id": outcome.MsgID, "error_kind": outcome.ErrorKind,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return store.ErrConflict
		}
		if err := tx.Table("delivery_plans").Where("event_id=? AND current_attempt_id=?", p.EventID, a.ID).Update("state", outcome.State).Error; err != nil {
			return err
		}
		a.State, a.OK, a.Verified, a.MsgID, a.ErrorKind = outcome.State, ok, outcome.Verified, outcome.MsgID, outcome.ErrorKind
		out = p.attempt(a)
		return nil
	})
	return out, queueFailure("记录投递结果失败", err)
}

func validPlanOutcome(o store.PlanOutcome) bool {
	switch o.State {
	case sender.StateAccepted:
		return o.OK && !strings.ContainsRune(o.MsgID, 0)
	case sender.StateUnknown, sender.StateNotPosted, sender.StateRejected, sender.StateDead, sender.StateSkipped, sender.StateManual:
		return !o.OK && !o.Verified && o.MsgID == ""
	default:
		return false
	}
}
