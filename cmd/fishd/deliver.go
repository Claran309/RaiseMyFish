package main

// Deliver 阶段：事件已经投递出去，只需要 Resume 核验并结清。

import (
	"context"
	"errors"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

// resumeDeps 是 Deliver / Verify handler 的只读依赖投影。
type resumeDeps struct {
	sender     sender.Sender
	channels   *channelSet
	bus        event.Bus
	retryDelay time.Duration
	log        *zap.Logger
}

// deliverHandler 返回 Deliver 阶段的调度 handler。
func deliverHandler(deps resumeDeps) scheduler.Handler {
	return func(ctx context.Context, w scheduler.Work) (scheduler.Result, error) {
		// 失败结清只能从 queued 出发：契约里没有 sent→dead 这条边
		// （deliver/verify 阶段领取时状态本来就是 queued）。
		from := []event.Status{event.StatusQueued}

		// Resume 同样走通道：注册表优先，取不到回落直连 sender
		disp, ok := deps.channels.resolve(w.Claim.Event.Channel, deps.sender)
		if !ok {
			return scheduler.Result{}, errors.New("找不到可用的发送通道")
		}
		res, err := disp.Resume(ctx, w.Claim)
		if err != nil {
			// 同 Send：sender 已结清并释放租约时不能再拿旧 claim 二次结清。
			if sender.Handled(err) {
				return scheduler.Result{Replied: res.OK}, nil
			}
			if settleErr := settleFailure(ctx, deps.bus, w.Claim, err, from, deps.retryDelay); settleErr != nil {
				return scheduler.Result{}, errors.Join(err, settleErr)
			}
			// 业务失败已结清（retry/dead）：可预期错误，返回 nil 让消费循环继续。
			// 只有结清失败（上面的 join）才是需要终止调度的基础设施错误。
			logWorkFailure(deps.log, "resume_settled", w, err)
			return scheduler.Result{Replied: res.OK}, nil
		}
		if err := settleDelivered(ctx, deps.bus, w.Claim, res.Verified, deps.retryDelay); err != nil {
			// Resume 成功同样意味着 sender 已原子结清；旧 claim 写 Bus 会报租约失效。
			if errors.Is(err, event.ErrLeaseLost) {
				return scheduler.Result{Replied: res.OK}, nil
			}
			return scheduler.Result{}, err
		}
		return scheduler.Result{Replied: res.OK}, nil
	}
}
