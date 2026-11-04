package main

// 本文件把「发送路径」收敛到通道注册表。
//
// 背景：Collect 早已走 Channel，但 Generate 的 Send 与 Deliver 的 Resume 仍直连 sender，
// 注册表只起采集作用。这里加一层解析：优先用注册表里的通道，取不到再回落直连 sender。
//
// 为什么保留 snd 兜底而不直接删掉：
//   - 注册表只登记「启用了采集器」的通道；只开发送不开采集时（例如私信当前默认关采集）通道不存在，
//     回落到路由 sender 才能继续回；测试里也大量直接传 fake sender，不必为此全量改造；
//   - 主路径（正常装配）一定命中通道，行为与改造前完全一致。

import (
	"context"
	"sync"

	"RaiseMyFish/internal/channel"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
)

// dispatcher 是 Channel 与 sender.Sender 的公共发送面。
//
// channel.Reply / channel.Result 是 sender 类型的别名，所以两者可以直接互换。
type dispatcher interface {
	Send(ctx context.Context, claim event.Claim, reply sender.Reply) (sender.Result, error)
	Resume(ctx context.Context, claim event.Claim) (sender.Result, error)
}

// channelSet 是「后注册」的通道表：deps 先拿到指针，main 装配完注册表再 Set。
//
// 这样不用改 buildConsume/buildConsumeWorkers 的签名顺序，也不会引入启动竞态：
// Set 发生在 Collect/servePipeline 之前，worker 真正取用一定在 Set 之后。
type channelSet struct {
	mu  sync.RWMutex
	reg *channel.Registry
}

// Set 绑定注册表（可重复调用，最后一次生效）。
func (s *channelSet) Set(reg *channel.Registry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reg = reg
}

// Get 取注册表；未绑定时返回 nil。
func (s *channelSet) Get() *channel.Registry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reg
}

// resolve 按事件通道找发送实现：注册表通道优先，其次直连 sender。
func (s *channelSet) resolve(ch event.Channel, fallback sender.Sender) (dispatcher, bool) {
	if reg := s.Get(); reg != nil {
		if c, ok := reg.Get(ch); ok && c != nil {
			return c, true
		}
	}
	if fallback != nil {
		return fallback, true
	}
	return nil, false
}
