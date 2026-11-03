package main

// Verify 阶段：与 Deliver 是同一件事——Resume 核验并结清；
// 调度器按 stage 分派到独立字段，所以两个 handler 都要显式装配。

import "RaiseMyFish/internal/scheduler"

// verifyHandler 返回 Verify 阶段的调度 handler。
func verifyHandler(deps resumeDeps) scheduler.Handler {
	return deliverHandler(deps)
}
