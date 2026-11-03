// Package channel 把「一个通道的采集 + 发送」收敛成统一接口与注册表，
// 为后续评论/私信通道铺路；本包只做薄封装，不重造 sender 类型。
package channel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
)

// Reply/Result 直接复用 sender 的既有类型，避免出现第二套发送契约。
type Reply = sender.Reply
type Result = sender.Result

// Channel 是一个可采集、可发送的通道。
type Channel interface {
	Name() event.Channel
	// Collect 启动采集（异步写入 bus），返回停止函数。
	// 立即返回的错误只表示参数/配置无效；采集运行期失败由实现自行记录。
	Collect(ctx context.Context, bus event.Bus) (stop func(), err error)
	// Send 发送一条回复；claim 是调度器当前租约快照，底层 sender 用它原子结清输入事件。
	Send(ctx context.Context, claim event.Claim, reply Reply) (Result, error)
}

// Registry 是按通道名索引的注册表；零值可直接使用。
type Registry struct {
	mu       sync.RWMutex
	channels map[event.Channel]Channel
}

// NewRegistry 创建一个空注册表；与零值等价，仅为装配处表达意图。
func NewRegistry() *Registry {
	return &Registry{channels: make(map[event.Channel]Channel)}
}

// Register 注册通道；nil、空名与重复注册都返回错误，不覆盖已有实例。
func (r *Registry) Register(ch Channel) error {
	if ch == nil {
		return errors.New("通道不能为 nil")
	}
	name := ch.Name()
	if name == "" {
		return errors.New("通道名不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.channels == nil {
		r.channels = make(map[event.Channel]Channel)
	}
	if _, exists := r.channels[name]; exists {
		return fmt.Errorf("通道 %s 已注册", name)
	}
	r.channels[name] = ch
	return nil
}

// Get 按通道名查找；未注册返回 false。
func (r *Registry) Get(name event.Channel) (Channel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ch, ok := r.channels[name]
	return ch, ok
}

// All 返回按通道名排序的快照；调用方修改切片不影响注册表。
func (r *Registry) All() []Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]Channel, 0, len(r.channels))
	for _, ch := range r.channels {
		all = append(all, ch)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name() < all[j].Name() })
	return all
}
