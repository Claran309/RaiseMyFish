package scheduler

import (
	"context"
	"errors"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

func copyWork(w Work) Work {
	w.Claim.Event.Meta = append([]byte(nil), w.Claim.Event.Meta...)
	if w.Task != nil {
		t := *w.Task
		if t.Deadline != nil {
			deadline := *t.Deadline
			t.Deadline = &deadline
		}
		w.Task = &t
	}
	return w
}
func (s *Scheduler) launch(parent context.Context, work Work) {
	ctx, cancel := context.WithTimeout(parent, s.options.WorkerTimeout)
	job := &running{work: work, cancel: cancel, renewAt: s.nextRenew(work.Claim)}
	s.active[work.Claim.Event.ID] = job
	s.sessions[session(work.Claim.Event)] = work.Claim.Event.ID
	handler := s.workers.Generate
	if work.Claim.Stage == event.StageVerify {
		handler = s.workers.Verify
	}
	workerInput := copyWork(work)
	go func() {
		done := completion{job: job}
		defer func() {
			if recover() != nil {
				done.err = errors.New("调度下游发生panic")
			}
			s.done <- done
		}()
		done.result, done.err = handler(ctx, workerInput)
	}()
}
func (s *Scheduler) complete(parent context.Context, done completion) error {
	job := done.job
	id := job.work.Claim.Event.ID
	if s.active[id] != job {
		return nil
	}
	job.cancel()
	delete(s.active, id)
	delete(s.sessions, session(job.work.Claim.Event))
	if job.lost || errors.Is(done.err, event.ErrLeaseLost) {
		// 正投递回执只保守收紧本地频率，不能用于改写新持有者的PG状态。
		if done.result.Replied {
			s.startCooldown(job.work.Claim.Event)
		}
		return nil
	}
	if done.err != nil {
		if done.result.Replied {
			s.startCooldown(job.work.Claim.Event)
		}
		return &safeError{"调度下游执行失败", done.err}
	}
	ctx, cancel := context.WithTimeout(parent, s.options.OperationTimeout)
	defer cancel()
	var receipt event.Claim
	if job.receipt != nil {
		receipt = *job.receipt
	} else {
		var err error
		receipt, err = s.repo.Snapshot(ctx, id)
		if err != nil {
			return err
		}
	}
	e := receipt.Event
	if e.ID != id || e.Channel != job.work.Claim.Event.Channel || e.Session != job.work.Claim.Event.Session || e.Private != job.work.Claim.Event.Private {
		return store.ErrConflict
	}
	if !ownSettlement(job, receipt) {
		if receipt.Lease != job.work.Claim.Lease && receipt.Lease.Version >= job.work.Claim.Lease.Version {
			if done.result.Replied {
				s.startCooldown(job.work.Claim.Event)
			}
			return nil // 外代的执行状态不能用于判定旧回调未结清。
		}
		return ErrUnsettled // 本代仍持有租约，必须经Bus结清后返回。
	}
	if done.result.Replied {
		s.startCooldown(e)
	}
	s.log.Debug("调度工作已结清", zap.String("event_id", id), zap.String("channel", string(e.Channel)), zap.String("result", string(e.Status)))
	return nil
}

func (s *Scheduler) startCooldown(e event.Event) {
	if p := s.policies[e.Channel]; p != nil && p.Cooldown > 0 {
		key := session(e)
		if old, ok := s.cooldowns[key]; ok {
			old.timer.Stop()
		}
		until := s.clock.Now().Add(p.Cooldown)
		s.cooldowns[key] = cooldown{until: until, timer: s.clock.AfterFunc(p.Cooldown, s.signal)}
	}
}

// 正常释放恰好递增一代；更高代或新Owner的完成不能冒作旧worker回执。
func ownSettlement(job *running, c event.Claim) bool {
	e := c.Event
	old := job.work.Claim
	return e.ID == old.Event.ID && e.Channel == old.Event.Channel && e.Session == old.Event.Session && e.Private == old.Event.Private && c.Lease.Owner == "" && c.Lease.Version == old.Lease.Version+1 && (e.Status == event.StatusDone || e.Status == event.StatusSkipped || e.Status == event.StatusDead || e.Status == event.StatusRetry)
}
func (s *Scheduler) wakeDelay() time.Duration {
	now := s.clock.Now()
	delay := s.options.PollInterval
	for _, item := range s.pending {
		delay = min(delay, max(time.Millisecond, item.renewAt.Sub(now)))
	}
	for _, job := range s.active {
		if !job.lost && job.receipt == nil {
			delay = min(delay, max(time.Millisecond, job.renewAt.Sub(now)))
		}
	}
	return delay
}

// Run只允许调用一次；所有回调必须合作取消，退出前等待它们返回再释放会话。
func (s *Scheduler) Run(ctx context.Context) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	if !s.started.CompareAndSwap(false, true) {
		return ErrStarted
	}
	defer func() {
		s.stopTimers()
		for _, job := range s.active {
			job.cancel()
		}
		for len(s.active) > 0 {
			done := <-s.done
			if s.active[done.job.work.Claim.Event.ID] == done.job {
				delete(s.active, done.job.work.Claim.Event.ID)
				delete(s.sessions, session(done.job.work.Claim.Event))
			}
		}
		// 未完成Claim保留PG租约，后续实例按既有到期恢复协议接手。
	}()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := s.cycle(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return &safeError{"调度循环失败", err}
		}
		timer := s.clock.AfterFunc(s.wakeDelay(), s.signal)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case done := <-s.done:
			timer.Stop()
			if err := s.complete(ctx, done); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return &safeError{"调度工作回执失败", err}
			}
		case <-s.wake:
			timer.Stop()
		case <-s.bus.Notify():
			timer.Stop()
		}
	}
}
