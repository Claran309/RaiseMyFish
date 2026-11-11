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
	"encoding/json"
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
	if !sender.ValidActor(key.AccountID) {
		return false
	}
	switch key.Channel {
	case event.ChComment:
		return sender.ValidActor(key.Destination)
	case event.ChDM:
		// Destination 就是事件 Session：新格式 "{接收账号}:{对方UID}"，
		// 旧数据只有对方 UID，两种都要能恢复。
		if sender.ValidActor(key.Destination) {
			return true
		}
		_, _, ok := event.SplitDMSession(key.Destination)
		return ok
	}
	return false
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
// 计划与阶段同事务推进：可投递尝试落到 deliver，armed/unknown 落到 verify。
// 崩溃恢复时调度器按阶段直接进入 Deliver/Verify→Resume，不再重跑 Brain/ReAct；
// 终态尝试（旧数据）保持原阶段，避免改写既有结算路径。
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
		// 旧数据的计划可能停在 generate；恢复建计划/换尝试时允许从此推进。
		if row.Stage != event.StageGenerate && row.Stage != event.StageDeliver {
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
			return updateClaim(tx, c, map[string]any{"stage": event.StageDeliver})
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
		if stage := planNextStage(row.Stage, a.State); stage != row.Stage {
			return updateClaim(tx, c, map[string]any{"stage": stage})
		}
		return nil
	})
	return out, queueFailure("保存投递计划失败", err)
}

// planNextStage 决定计划落库后的执行阶段。
//
// ready/not_posted 还需要 POST，切到 deliver；armed/unknown 可能已到平台，
// 只允许 verify 核验；终态保持原阶段，兼容旧数据的结算路径。
func planNextStage(current event.Stage, state string) event.Stage {
	switch state {
	case sender.StateReady, sender.StateNotPosted:
		return event.StageDeliver
	case sender.StateArmed, sender.StateUnknown:
		return event.StageVerify
	default:
		return current
	}
}

// PlanAccount 返回计划绑定的账号（B站 MID）。
//
// 恢复旧尝试必须先读它：当前活跃账号可能已经切走，key 一旦用新号重建，
// 就会变成「用新账号重发旧任务」。
func (r *PlannedRepo) PlanAccount(ctx context.Context, eventID string) (string, error) {
	if ctx == nil || strings.TrimSpace(eventID) == "" {
		return "", store.ErrInvalid
	}
	var row struct {
		AccountID string `gorm:"column:account_id"`
	}
	err := r.db.WithContext(ctx).Table("delivery_plans").Select("account_id").
		Where("event_id=?", eventID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", store.ErrNotFound
	}
	if err != nil {
		return "", &safeError{"读取投递计划账号失败", err}
	}
	return row.AccountID, nil
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
		// 取得许可即进入 verify：POST 期间崩溃时恢复只能核验，不能重新生成或重投。
		if row.Stage != event.StageDeliver {
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
		if err := updateClaim(tx, c, map[string]any{"stage": event.StageVerify}); err != nil {
			return err
		}
		a.State, a.ArmedAt, a.PermitUntil = sender.StateArmed, &now, &until
		out = p.attempt(a)
		return nil
	})
	return out, queueFailure("授予投递许可失败", err)
}

// planAuditAccepted 写评论/私信成功投递的审计事实。
//
// 正文取计划快照（平台实际收到的文字），平台ID进 send_log.msg_id；
// 只有验收到稳定回执才算「已发言」：未验证的接受事实让 out 保持 retry，
// 不把「可能发出」写成 done。平台已接受且回执稳定、只是没完成核验的，
// 标 sent_unconfirmed（已发送（未确认）），与真正没有回执的 unknown_manual 区分。
func planAuditAccepted(tx *gorm.DB, p deliveryPlanRow, a deliveryAttemptRow) error {
	at := a.CreatedAt
	atKind := "local_attempt"
	if a.ArmedAt != nil {
		at = *a.ArmedAt
		atKind = "permit_observed"
	}
	receipt := planReceiptStable(a.MsgID)
	evidence := "none"
	if a.Verified || receipt {
		evidence = "platform_receipt"
	}
	outStatus := event.StatusDone
	meta, err := json.Marshal(map[string]any{
		"attempt_id": a.ID,
		"input_id":   p.EventID,
		"outcome":    a.State,
		"error_kind": a.ErrorKind,
		"verified":   a.Verified,
		"evidence":   evidence,
		"at_kind":    atKind,
	})
	if err != nil {
		return err
	}
	if !a.Verified {
		outStatus = event.StatusRetry
		var fields map[string]any
		if err := json.Unmarshal(meta, &fields); err != nil {
			return err
		}
		if receipt {
			fields["sent_unconfirmed"] = true
		} else {
			fields["unknown_manual"] = true
		}
		if meta, err = json.Marshal(fields); err != nil {
			return err
		}
	}
	if err := tx.Exec(`INSERT INTO send_log(event_id,channel,ok,verified,msg_id,error,at,attempt_id) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(attempt_id) DO UPDATE SET event_id=excluded.event_id,channel=excluded.channel,ok=excluded.ok,verified=excluded.verified,msg_id=excluded.msg_id,error=excluded.error,at=excluded.at`,
		p.EventID, p.Channel, a.OK, a.Verified, a.MsgID, a.ErrorKind, at, a.ID).Error; err != nil {
		return err
	}
	outID := p.Channel + ":out:" + p.Destination + ":" + a.ID
	return tx.Exec(`INSERT INTO events(id,channel,dir,session,from_id,text,private,reply_to,at,status,meta,stage) VALUES(?,?,?,?,?,?,?,?,?,?,?,'verify') ON CONFLICT(id) DO UPDATE SET status=excluded.status,meta=excluded.meta,at=excluded.at,status_changed_at=CASE WHEN events.status<>excluded.status THEN clock_timestamp() ELSE events.status_changed_at END`,
		outID, p.Channel, event.DirOut, p.Destination, p.AccountID, p.Text, p.Channel == string(event.ChDM), p.EventID, at, outStatus, string(meta)).Error
}

// planSettleAccepted 在同一事务里结清输入事件。
//
// 平台已接受且有稳定回执时按 done 结清（含未完成核验的「已发送（未确认）」），
// 不再重投；POST 期间租约可能刚好过期，审计事实仍要落库，输入交给恢复流程
// 补结算，不能用过期租约强行改写别人的行。
func planSettleAccepted(tx *gorm.DB, c event.Claim, p deliveryPlanRow) error {
	return planSettleInput(tx, c, p, event.StatusDone)
}

// planSettleUnconfirmed 结清「平台明确接受但缺稳定回执」的输入事件：
// dead 停止自动处理，不冒充 done，也不交回重投，核对留给人工。
func planSettleUnconfirmed(tx *gorm.DB, c event.Claim, p deliveryPlanRow) error {
	return planSettleInput(tx, c, p, event.StatusDead)
}

func planSettleInput(tx *gorm.DB, c event.Claim, p deliveryPlanRow, status event.Status) error {
	return tx.Exec(`UPDATE events SET status=?,stage=?,retry_at=NULL,status_changed_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,lease_version=lease_version+1 WHERE id=? AND lease_owner=? AND lease_version=? AND lease_until>clock_timestamp() AND status IN (?,?,?)`,
		status, event.StageVerify, p.EventID, c.Lease.Owner, c.Lease.Version,
		event.StatusQueued, event.StatusReplying, event.StatusSent).Error
}

// planReceiptStable 判断已落库回执是否足以支撑 done。
// 与 durable 的上层判定同口径：空白、全零、含 NUL 都不算稳定回执。
func planReceiptStable(id string) bool {
	id = strings.TrimSpace(id)
	if strings.ContainsRune(id, 0) {
		return false
	}
	return strings.Trim(id, "0") != ""
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
			// 旧数据可能把「平台接受」记成了已验证：没有稳定回执就纠正为未验证，
			// 结清保持人工核对，不冒充 done。
			if a.Verified && !planReceiptStable(a.MsgID) {
				a.Verified = false
				if a.ErrorKind == "" {
					a.ErrorKind = "unconfirmed"
				}
				if err := tx.Table("delivery_attempts").Where("id=?", a.ID).Updates(map[string]any{
					"verified": false, "error_kind": a.ErrorKind,
				}).Error; err != nil {
					return err
				}
			}
			if err := planAuditAccepted(tx, p, a); err != nil {
				return err
			}
			// 有稳定回执就是已发送（未确认），输入按 done 结清；只有真正
			// 没有回执的接受事实才保持 unknown，停止自动处理等人工核对。
			if a.Verified || planReceiptStable(a.MsgID) {
				if err := planSettleAccepted(tx, c, p); err != nil {
					return err
				}
			} else if err := planSettleUnconfirmed(tx, c, p); err != nil {
				return err
			}
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
		if outcome.State == sender.StateAccepted && !planReceiptStable(outcome.MsgID) {
			// 平台接受但没有稳定回执：Verified 不能成立，落库前统一纠正。
			outcome.Verified = false
			if outcome.ErrorKind == "" {
				outcome.ErrorKind = "unconfirmed"
			}
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
		if outcome.State == sender.StateNotPosted {
			// 可信未投递还要再 POST：把阶段退回 deliver，恢复时才会走投递而不是核验。
			// 租约已失效时不改阶段，交给恢复按未知保守停止。
			if err := tx.Table("events").Where("id=? AND lease_owner=? AND lease_version=? AND lease_until>clock_timestamp() AND stage=?", p.EventID, c.Lease.Owner, c.Lease.Version, event.StageVerify).Update("stage", event.StageDeliver).Error; err != nil {
				return err
			}
		}
		a.State, a.OK, a.Verified, a.MsgID, a.ErrorKind = outcome.State, ok, outcome.Verified, outcome.MsgID, outcome.ErrorKind
		if outcome.State == sender.StateAccepted {
			if err := planAuditAccepted(tx, p, a); err != nil {
				return err
			}
			if a.Verified || planReceiptStable(a.MsgID) {
				if err := planSettleAccepted(tx, c, p); err != nil {
					return err
				}
			} else if err := planSettleUnconfirmed(tx, c, p); err != nil {
				return err
			}
		}
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
