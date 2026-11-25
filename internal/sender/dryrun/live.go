package dryrun

// 本文件让 dry-run 复用真实 live 发送器的投递计划与 send_log 记账，
// 只把最外层 transport 换成不触网、固定成功的本地实现。

import (
	"context"
	"fmt"
	"sync/atomic"

	"RaiseMyFish/internal/sender"
	senderslive "RaiseMyFish/internal/sender/live"
)

// LiveTransport 是 sender/live.Transport 的本地替身。
type LiveTransport struct {
	seq atomic.Uint64
}

// NewLiveTransport 构造无网络 transport。
func NewLiveTransport() *LiveTransport { return &LiveTransport{} }

func (t *LiveTransport) Open(context.Context, sender.Attempt) (senderslive.Session, error) {
	return &liveSession{transport: t}, nil
}

func (t *LiveTransport) Verify(context.Context, sender.Attempt) (sender.Verdict, error) {
	return t.verdict(), nil
}

type liveSession struct {
	transport *LiveTransport
}

func (s *liveSession) Post(context.Context, sender.Permit) (sender.Verdict, error) {
	return s.transport.verdict(), nil
}

func (s *liveSession) Close() error { return nil }

func (t *LiveTransport) verdict() sender.Verdict {
	return sender.Verdict{
		Outcome:  sender.OutcomeAccepted,
		MsgID:    fmt.Sprintf("dry-%d", t.seq.Add(1)),
		Verified: true,
		Evidence: "receipt_id",
	}
}
