package store

import (
	"context"
	"time"

	"RaiseMyFish/internal/event"
)

// TimingRepo 只读写分阶段时间，不承载正文或业务状态。
//
// 采集/排队/领取/发送/核验由数据库触发器自动记录；生成完成时间由调用方写入。
type TimingRepo interface {
	Get(context.Context, string) (event.Timing, error)
	MarkGenerated(context.Context, string, time.Time) error
}
