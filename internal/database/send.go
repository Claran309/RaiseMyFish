package database

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"math/big"
	"strings"
	"time"
)

// SendRepo 将投递许可、审计和输入租约置于同一个PG事务。
type SendRepo struct {
	db     *gorm.DB
	policy sender.Policy
}

// NewSendRepo 只注入连接和已验证策略，不自动迁移。
func NewSendRepo(db *gorm.DB, policy sender.Policy) (*SendRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &SendRepo{db: db, policy: policy}, nil
}

type sendPlan struct {
	EventID          string
	ActorID          string
	RoomID           string
	Text             string
	TextSHA          string `gorm:"column:text_sha"`
	State            string
	CurrentAttemptID string
	AttemptCount     int
}

type sendRecord struct {
	sender.Attempt `gorm:"embedded"`
	TextSHA        string `gorm:"column:text_sha"`
	ArmedAt        *time.Time
	ArmedOwner     *string
	ArmedVersion   *int64
	OK             *bool
}

func sendActorLock(tx *gorm.DB, actor string) error {
	// 同账号跨房间串行许可；哈希碰撞只增加等待，不放宽范围。
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtext(current_database()||':'||current_schema()||':sender'),hashtext(?))", actor).Error
}
func sendActual(row queueRow) error {
	e := row.Event
	if e.Channel != event.ChLive || e.Dir != event.DirIn || e.Private || !sender.ValidActor(e.Session) {
		return store.ErrInvalid
	}
	if e.Status != event.StatusQueued && e.Status != event.StatusReplying && e.Status != event.StatusSent {
		return store.ErrConflict
	}
	return nil
}
func sendCurrent(tx *gorm.DB, row queueRow, actor, attemptID string) (sendPlan, sendRecord, error) {
	var p sendPlan
	err := tx.Table("send_plans").Where("event_id=?", row.Event.ID).Take(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return p, sendRecord{}, store.ErrConflict
	}
	if err != nil {
		return p, sendRecord{}, err
	}
	if (actor != "" && p.ActorID != actor) || p.RoomID != row.Event.Session || (attemptID != "" && p.CurrentAttemptID != attemptID) {
		return p, sendRecord{}, store.ErrConflict
	}
	var a sendRecord
	err = tx.Table("send_attempts").Where("id=? AND event_id=?", p.CurrentAttemptID, p.EventID).Take(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = store.ErrConflict
	}
	a.Text = p.Text
	if err == nil && (a.ActorID != p.ActorID || a.RoomID != p.RoomID || a.TextSHA != p.TextSHA) {
		err = store.ErrConflict
	}
	return p, a, err
}
func sendUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成发送尝试标识失败：%w", err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func sendNow(tx *gorm.DB) (time.Time, error) {
	var now time.Time
	err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error
	return now, err
}
func sendNewAttempt(tx *gorm.DB, p sendPlan) (sendRecord, error) {
	id, err := sendUUID()
	if err != nil {
		return sendRecord{}, err
	}
	now, err := sendNow(tx)
	if err != nil {
		return sendRecord{}, err
	}
	a := sendRecord{Attempt: sender.Attempt{ID: id, EventID: p.EventID, ActorID: p.ActorID, RoomID: p.RoomID, Text: p.Text, State: sender.StateReady, Number: p.AttemptCount, CreatedAt: now, Evidence: "none"}, TextSHA: p.TextSHA}
	err = tx.Exec(`INSERT INTO send_attempts(id,event_id,actor_id,room_id,text_sha,number,created_at) VALUES(?,?,?,?,?,?,?)`, a.ID, a.EventID, a.ActorID, a.RoomID, a.TextSHA, a.Number, a.CreatedAt).Error
	return a, err
}

// Prepare 首次保存回复，后续调用只允许返回原快照。
func (r *SendRepo) Prepare(ctx context.Context, c event.Claim, reply sender.Reply, actor string) (sender.Attempt, error) {
	if ctx == nil || !validClaim(c) || !sender.ValidActor(actor) {
		return sender.Attempt{}, store.ErrInvalid
	}
	if err := reply.Validate(c); err != nil {
		return sender.Attempt{}, err
	}
	var a sendRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if err := sendActual(row); err != nil {
			return err
		}
		if err := reply.Validate(row.claim()); err != nil {
			return err
		}
		if err := sendActorLock(tx, actor); err != nil {
			return err
		}
		var p sendPlan
		err = tx.Table("send_plans").Where("event_id=?", row.Event.ID).Take(&p).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if row.Stage != event.StageGenerate || row.Event.Status != event.StatusReplying {
				return store.ErrConflict
			}
			hash := sha256.Sum256([]byte(reply.Text))
			id, err := sendUUID()
			if err != nil {
				return err
			}
			p = sendPlan{EventID: row.Event.ID, ActorID: actor, RoomID: row.Event.Session, Text: reply.Text, TextSHA: hex.EncodeToString(hash[:]), State: sender.StateReady, CurrentAttemptID: id, AttemptCount: 1}
			if err := tx.Table("send_plans").Create(&p).Error; err != nil {
				return err
			}
			a, err = sendNewAttempt(tx, p)
			if err != nil {
				return err
			}
			if err := tx.Table("send_plans").Where("event_id=?", p.EventID).Update("current_attempt_id", a.ID).Error; err != nil {
				return err
			}
		} else {
			if err != nil {
				return err
			}
			if p.ActorID != actor || p.RoomID != row.Event.Session || p.Text != reply.Text {
				return store.ErrConflict
			}
			if row.Stage != event.StageDeliver && row.Stage != event.StageVerify {
				return store.ErrConflict
			}
			_, a, err = sendCurrent(tx, row, actor, "")
			if err != nil {
				return err
			}
		}
		if a.State == sender.StateReady {
			duplicate, err := r.sendDuplicate(tx, a)
			if err != nil {
				return err
			}
			if duplicate {
				return r.sendSkip(tx, c, &a, event.StageDeliver)
			}
		}
		if err := sendAudit(tx, a, event.StatusRetry); err != nil {
			return err
		}
		stage := row.Stage
		if stage == event.StageGenerate {
			stage = event.StageDeliver
		}
		return updateClaim(tx, c, map[string]any{"stage": stage})
	})
	return a.Attempt, queueFailure("保存发送计划失败", err)
}

// Load 只为明确未发出/明确拒绝的投递分支创建下一次尝试。
func (r *SendRepo) Load(ctx context.Context, c event.Claim, actor string) (sender.Attempt, error) {
	if ctx == nil || !validClaim(c) || !sender.ValidActor(actor) {
		return sender.Attempt{}, store.ErrInvalid
	}
	var a sendRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if err := sendActual(row); err != nil {
			return err
		}
		if row.Stage != event.StageDeliver && row.Stage != event.StageVerify {
			return store.ErrConflict
		}
		if err := sendActorLock(tx, actor); err != nil {
			return err
		}
		p, current, err := sendCurrent(tx, row, actor, "")
		if err != nil {
			return err
		}
		a = current
		if row.Stage == event.StageDeliver && (a.State == sender.StateNotPosted || a.State == sender.StateRejected) {
			if p.AttemptCount >= r.policy.MaxAttempts {
				return store.ErrConflict
			}
			p.AttemptCount++
			a, err = sendNewAttempt(tx, p)
			if err != nil {
				return err
			}
			if err := tx.Table("send_plans").Where("event_id=?", p.EventID).Updates(map[string]any{"current_attempt_id": a.ID, "attempt_count": p.AttemptCount, "state": sender.StateReady}).Error; err != nil {
				return err
			}
			if err := sendAudit(tx, a, event.StatusRetry); err != nil {
				return err
			}
		}
		if row.Stage == event.StageDeliver {
			if a.State != sender.StateReady {
				return store.ErrConflict
			}
			duplicate, err := r.sendDuplicate(tx, a)
			if err != nil {
				return err
			}
			if duplicate {
				return r.sendSkip(tx, c, &a, row.Stage)
			}
		} else if a.State != sender.StateArmed && a.State != sender.StateUnknown && a.State != sender.StateAccepted {
			return store.ErrConflict
		}
		return updateClaim(tx, c, map[string]any{"stage": row.Stage})
	})
	return a.Attempt, queueFailure("读取发送计划失败", err)
}

func (r *SendRepo) sendDuplicate(tx *gorm.DB, a sendRecord) (bool, error) {
	var duplicate bool
	err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM send_attempts WHERE actor_id=? AND room_id=? AND text_sha=? AND id<>? AND state IN ('armed','unknown','accepted','manual') AND permit_until+(? * interval '1 microsecond')>clock_timestamp())`, a.ActorID, a.RoomID, a.TextSHA, a.ID, r.policy.DuplicateWindow.Microseconds()).Scan(&duplicate).Error
	return duplicate, err
}
func sendSaveAttempt(tx *gorm.DB, a sendRecord) error {
	return tx.Table("send_attempts").Where("id=?", a.ID).Updates(map[string]any{"state": a.State, "nonce": nullableNonce(a.Nonce), "permit_until": a.PermitUntil, "armed_at": a.ArmedAt, "armed_owner": a.ArmedOwner, "armed_version": a.ArmedVersion, "verify_count": a.VerifyCount, "prep_failures": a.PrepFailures, "ok": a.OK, "verified": a.Verified, "msg_id": a.MsgID, "evidence": a.Evidence, "error_kind": a.ErrorKind}).Error
}
func nullableNonce(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func sendAudit(tx *gorm.DB, a sendRecord, status event.Status) error {
	at := a.CreatedAt
	atKind := "local_attempt"
	if a.ArmedAt != nil {
		at = *a.ArmedAt
		atKind = "permit_observed"
	}
	// 仅持久化白名单字段；Reply.Meta和平台原始响应不进入审计。
	meta, err := json.Marshal(map[string]any{"attempt_id": a.ID, "input_id": a.EventID, "outcome": a.State, "error_kind": a.ErrorKind, "verified": a.Verified, "evidence": a.Evidence, "at_kind": atKind})
	if err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO send_log(event_id,channel,ok,verified,msg_id,error,at,attempt_id) VALUES(?,'live',?,?,?,?,?,?) ON CONFLICT(attempt_id) DO UPDATE SET ok=excluded.ok,verified=excluded.verified,msg_id=excluded.msg_id,error=excluded.error,at=excluded.at`, a.EventID, a.OK, a.Verified, a.MsgID, a.ErrorKind, at, a.ID).Error; err != nil {
		return err
	}
	return tx.Exec(`INSERT INTO events(id,channel,dir,session,from_id,text,private,reply_to,at,status,meta,stage) VALUES(?,'live','out',?,?,?,false,?,?,?,?, 'verify') ON CONFLICT(id) DO UPDATE SET status=excluded.status,meta=excluded.meta,at=excluded.at,status_changed_at=CASE WHEN events.status<>excluded.status THEN clock_timestamp() ELSE events.status_changed_at END`, "live:out:"+a.RoomID+":"+a.ID, a.RoomID, a.ActorID, a.Text, a.EventID, at, status, string(meta)).Error
}
func sendRelease(tx *gorm.DB, c event.Claim, status event.Status, stage event.Stage, until *time.Time) error {
	changes := map[string]any{"status": status, "stage": stage, "status_changed_at": gorm.Expr("clock_timestamp()"), "retry_at": until}
	releaseLease(changes)
	return updateClaim(tx, c, changes)
}
func (r *SendRepo) sendSkip(tx *gorm.DB, c event.Claim, a *sendRecord, stage event.Stage) error {
	a.State = sender.StateSkipped
	a.ErrorKind = "duplicate_window"
	ok := false
	a.OK = &ok
	if err := sendSaveAttempt(tx, *a); err != nil {
		return err
	}
	if err := tx.Table("send_plans").Where("event_id=?", a.EventID).Update("state", sender.StateSkipped).Error; err != nil {
		return err
	}
	if err := sendAudit(tx, *a, event.StatusSkipped); err != nil {
		return err
	}
	return sendRelease(tx, c, event.StatusSkipped, stage, nil)
}

// Arm 在账号事务锁内重复检查防重和间隔，提交后才允许执行一次POST。
func (r *SendRepo) Arm(ctx context.Context, c event.Claim, id string) (sender.Permit, error) {
	if ctx == nil || !validClaim(c) || id == "" {
		return sender.Permit{}, store.ErrInvalid
	}
	var permit sender.Permit
	var handled error
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if err := sendActual(row); err != nil {
			return err
		}
		if row.Stage != event.StageDeliver || (row.Event.Status != event.StatusQueued && row.Event.Status != event.StatusReplying) {
			return store.ErrConflict
		}
		_, a, err := sendCurrent(tx, row, "", id)
		if err != nil {
			return err
		}
		if a.State != sender.StateReady {
			return store.ErrConflict
		}
		if !sender.ValidActor(a.ActorID) {
			return store.ErrInvalid
		}
		if err := sendActorLock(tx, a.ActorID); err != nil {
			return err
		}
		duplicate, err := r.sendDuplicate(tx, a)
		if err != nil {
			return err
		}
		if duplicate {
			if err := r.sendSkip(tx, c, &a, row.Stage); err != nil {
				return err
			}
			handled = &sender.Error{Kind: sender.KindSkipped, Settled: true}
			return nil
		}
		var waits []struct{ Until time.Time }
		if err := tx.Raw(`SELECT permit_until+(? * interval '1 microsecond') AS until FROM send_attempts WHERE actor_id=? AND id<>? AND armed_at IS NOT NULL AND state<>'not_posted' AND permit_until+(? * interval '1 microsecond')>clock_timestamp() ORDER BY permit_until DESC LIMIT 1`, r.policy.MinInterval.Microseconds(), a.ActorID, a.ID, r.policy.MinInterval.Microseconds()).Scan(&waits).Error; err != nil {
			return err
		}
		if len(waits) > 0 {
			_, handled, err = r.sendDefer(tx, c, &a, sender.Deferral{Kind: sender.KindRetry, Code: "rate_wait", RetryAt: waits[0].Until})
			return err
		}
		now, err := sendNow(tx)
		if err != nil {
			return err
		}
		until := now.Add(r.policy.PermitTTL)
		if row.Until == nil {
			return event.ErrLeaseLost
		}
		if row.Until.Before(until) {
			until = *row.Until
		}
		if !until.After(now) {
			return event.ErrLeaseLost
		}
		n, err := rand.Int(rand.Reader, big.NewInt(9223372036854775807))
		if err != nil {
			return err
		}
		n.Add(n, big.NewInt(1))
		a.Nonce = n.String()
		a.State = sender.StateArmed
		a.ArmedAt = &now
		owner, version := c.Lease.Owner, c.Lease.Version
		a.ArmedOwner = &owner
		a.ArmedVersion = &version
		a.PermitUntil = &until
		a.OK = nil
		if err := sendSaveAttempt(tx, a); err != nil {
			return err
		}
		if err := sendAudit(tx, a, event.StatusRetry); err != nil {
			return err
		}
		if err := tx.Table("send_plans").Where("event_id=?", a.EventID).Update("state", sender.StateArmed).Error; err != nil {
			return err
		}
		if err := updateClaim(tx, c, map[string]any{"status": event.StatusReplying, "stage": event.StageVerify, "status_changed_at": gorm.Expr("CASE WHEN status<>'replying' THEN clock_timestamp() ELSE status_changed_at END")}); err != nil {
			return err
		}
		permit = sender.Permit{AttemptID: a.ID, RequestID: a.ID, Nonce: a.Nonce, ExpiresAt: until}
		return nil
	})
	if err != nil {
		return sender.Permit{}, queueFailure("授予发送许可失败", err)
	}
	return permit, handled
}

func sendDLQ(tx *gorm.DB, a sendRecord, stage event.Stage) error {
	payload, err := json.Marshal(map[string]string{"attempt_id": a.ID, "input_id": a.EventID})
	if err != nil {
		return err
	}
	return tx.Exec("INSERT INTO dlq(event_id,stage,error,payload) VALUES(?,?,?,?::jsonb)", a.EventID, stage, a.ErrorKind, string(payload)).Error
}
func (r *SendRepo) sendDefer(tx *gorm.DB, c event.Claim, a *sendRecord, d sender.Deferral) (sender.Result, error, error) {
	now, err := sendNow(tx)
	if err != nil {
		return sender.Result{}, nil, err
	}
	until := d.RetryAt
	if until.IsZero() {
		until = now.Add(r.policy.RetryDelay)
	}
	if !until.After(now) {
		return sender.Result{}, nil, store.ErrInvalid
	}
	a.ErrorKind = d.Code
	if d.CountFailure {
		a.PrepFailures++
	}
	status := event.StatusRetry
	kind := d.Kind
	planState := sender.StateReady
	if a.PrepFailures >= r.policy.MaxAttempts {
		status = event.StatusDead
		a.State = sender.StateDead
		planState = sender.StateDead
		ok := false
		a.OK = &ok
		if kind != sender.KindAuth {
			kind = sender.KindDead
		}
	}
	if err := sendSaveAttempt(tx, *a); err != nil {
		return sender.Result{}, nil, err
	}
	if err := tx.Table("send_plans").Where("event_id=?", a.EventID).Update("state", planState).Error; err != nil {
		return sender.Result{}, nil, err
	}
	if err := sendAudit(tx, *a, status); err != nil {
		return sender.Result{}, nil, err
	}
	if status == event.StatusDead {
		if err := sendDLQ(tx, *a, event.StageDeliver); err != nil {
			return sender.Result{}, nil, err
		}
		if err := sendRelease(tx, c, status, event.StageDeliver, nil); err != nil {
			return sender.Result{}, nil, err
		}
	} else if err := sendRelease(tx, c, status, event.StageDeliver, &until); err != nil {
		return sender.Result{}, nil, err
	}
	return a.Result(), &sender.Error{Kind: kind, RetryAt: until, Settled: true}, nil
}

// Defer 只处理尚无投递许可的ready；普通限流等待不消耗预检失败次数。
func (r *SendRepo) Defer(ctx context.Context, c event.Claim, id string, d sender.Deferral) (sender.Result, error) {
	if ctx == nil || !validClaim(c) || id == "" || !sender.SafeCode(d.Code) || (d.Kind != sender.KindRetry && d.Kind != sender.KindAuth) {
		return sender.Result{}, store.ErrInvalid
	}
	var result sender.Result
	var handled error
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if err := sendActual(row); err != nil {
			return err
		}
		if row.Stage != event.StageDeliver {
			return store.ErrConflict
		}
		_, a, err := sendCurrent(tx, row, "", id)
		if err != nil {
			return err
		}
		if a.State != sender.StateReady {
			return store.ErrConflict
		}
		result, handled, err = r.sendDefer(tx, c, &a, d)
		return err
	})
	if err != nil {
		return sender.Result{}, queueFailure("延期投递失败", err)
	}
	return result, handled
}

// Settle 核验的任何非成功结果都保持未知，只有POST明确否定能回deliver。
func (r *SendRepo) Settle(ctx context.Context, c event.Claim, id string, v sender.Verdict) (sender.Result, error) {
	if ctx == nil || !validClaim(c) || id == "" {
		return sender.Result{}, store.ErrInvalid
	}
	if err := v.Validate(); err != nil {
		return sender.Result{}, err
	}
	var result sender.Result
	var handled error
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if err := sendActual(row); err != nil {
			return err
		}
		if row.Stage != event.StageVerify {
			return store.ErrConflict
		}
		_, a, err := sendCurrent(tx, row, "", id)
		if err != nil {
			return err
		}
		if a.Verified || (a.State != sender.StateArmed && a.State != sender.StateUnknown && a.State != sender.StateAccepted) {
			return store.ErrConflict
		}
		// POST回执只属于最初取得许可的有效租约，恢复后的worker只能核验。
		if v.Origin == sender.OriginPost && (a.State != sender.StateArmed || a.ArmedOwner == nil || a.ArmedVersion == nil || *a.ArmedOwner != c.Lease.Owner || *a.ArmedVersion != c.Lease.Version) {
			return store.ErrConflict
		}
		if a.MsgID != "" && (v.MsgID != a.MsgID || (v.Origin == sender.OriginVerify && v.Verified && v.Evidence != "history_id")) {
			// 任意来源都不能替换已知ID；已知ID核验必须采用精确ID证据。
			v.Outcome = sender.OutcomeUnknown
			v.Verified = false
			v.MsgID = ""
			v.Evidence = "none"
		}
		// 失败核验可能没有ID；保留已知稳定ID供下一次精确回读。
		if v.MsgID != "" && strings.Trim(v.MsgID, "0") != "" {
			a.MsgID = v.MsgID
		}
		a.Verified = v.Verified
		a.Evidence = v.Evidence
		if a.Evidence == "" {
			a.Evidence = "none"
		}
		a.ErrorKind = v.ErrorKind
		inputStatus := event.StatusRetry
		outStatus := event.StatusRetry
		stage := event.StageVerify
		planState := sender.StateUnknown
		kind := sender.KindUncertain
		ok := false
		switch {
		case v.Verified:
			a.State = sender.StateAccepted
			yes := true
			a.OK = &yes
			a.ErrorKind = ""
			inputStatus = event.StatusDone
			outStatus = event.StatusDone
			planState = "delivered"
		case v.Origin == sender.OriginPost && (v.Outcome == sender.OutcomeRejected || v.Outcome == sender.OutcomeNotPosted) && (v.ErrorKind == "content" || v.ErrorKind == "invalid" || v.ErrorKind == "unavailable"):
			a.State = sender.StateDead
			inputStatus = event.StatusDead
			outStatus = event.StatusDead
			planState = sender.StateDead
			kind = sender.KindDead
			a.OK = &ok
			if v.ErrorKind == "unavailable" {
				a.State = sender.StateSkipped
				inputStatus = event.StatusSkipped
				outStatus = event.StatusSkipped
				planState = sender.StateSkipped
				kind = sender.KindSkipped
			}
		case v.Origin == sender.OriginPost && (v.Outcome == sender.OutcomeNotPosted || (v.Outcome == sender.OutcomeRejected && (v.ErrorKind == "rate_limit" || v.ErrorKind == "auth"))):
			a.State = sender.StateNotPosted
			if v.Outcome == sender.OutcomeRejected {
				a.State = sender.StateRejected
			}
			a.OK = &ok
			kind = sender.KindRetry
			if v.ErrorKind == "auth" {
				kind = sender.KindAuth
			}
			if a.Number < r.policy.MaxAttempts {
				stage = event.StageDeliver
				planState = sender.StateReady
			} else {
				inputStatus = event.StatusDead
				outStatus = event.StatusDead
				planState = sender.StateDead
				if kind != sender.KindAuth {
					kind = sender.KindDead
				}
			}
		default:
			a.State = sender.StateUnknown
			a.OK = nil
			if a.ErrorKind == "" {
				a.ErrorKind = "unconfirmed"
			}
			if v.Origin == sender.OriginPost && v.Outcome == sender.OutcomeAccepted && v.MsgID != "" && strings.Trim(v.MsgID, "0") != "" {
				a.State = sender.StateAccepted
				yes := true
				a.OK = &yes
			}
			if v.Origin == sender.OriginVerify {
				a.VerifyCount++
			}
			if a.VerifyCount >= r.policy.MaxVerifications {
				a.State = sender.StateManual
				a.OK = nil
				a.ErrorKind = "unknown_manual"
				inputStatus = event.StatusDead
				planState = sender.StateManual
			}
		}
		if err := sendSaveAttempt(tx, a); err != nil {
			return err
		}
		if err := tx.Table("send_plans").Where("event_id=?", a.EventID).Update("state", planState).Error; err != nil {
			return err
		}
		if err := sendAudit(tx, a, outStatus); err != nil {
			return err
		}
		if inputStatus == event.StatusDead {
			if err := sendDLQ(tx, a, stage); err != nil {
				return err
			}
		}
		var retryAt *time.Time
		if inputStatus == event.StatusRetry {
			now, err := sendNow(tx)
			if err != nil {
				return err
			}
			until := now.Add(r.policy.RetryDelay)
			retryAt = &until
		}
		if err := sendRelease(tx, c, inputStatus, stage, retryAt); err != nil {
			return err
		}
		result = a.Result()
		if inputStatus != event.StatusDone {
			e := &sender.Error{Kind: kind, Settled: true}
			if retryAt != nil {
				e.RetryAt = *retryAt
			}
			handled = e
		}
		return nil
	})
	if err != nil {
		return sender.Result{}, queueFailure("结算发送结果失败", err)
	}
	return result, handled
}
