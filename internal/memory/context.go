// Package memory 定义P0只读上下文、纯校验与规则版提炼（0.9a）；不实现召回查询、worker 或语义理解。
package memory

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"RaiseMyFish/internal/event"
)

// Scope 是可信调用方提供的目标会话范围。
type Scope struct {
	Channel event.Channel
	Session string
	Private bool
}

// Message 是已过滤消息的最小投影，不包含平台Meta。
//
// ReplyTo 保存 out 事件指向的入向事件 ID：发送链路把它写成触发回复的输入事件，
// 因而是「鱼这一句回的是哪一句」的持久证据，焦点轮次按它配对。
type Message struct {
	EventID  string        `json:"event_id"`
	Channel  event.Channel `json:"channel"`
	Session  string        `json:"session"`
	Dir      event.Dir     `json:"dir"`
	FromID   string        `json:"from_id,omitempty"`
	FromName string        `json:"from_name,omitempty"`
	Text     string        `json:"text"`
	ReplyTo  string        `json:"reply_to,omitempty"`
	Private  bool          `json:"private"`
	At       time.Time     `json:"at"`
}

// Record 是已查询到当前有效记录的摘要投影。
type Record struct {
	ID         string
	Channel    event.Channel
	Session    string
	Summary    string
	Importance float64
	Confidence float64
	Private    bool
	EventIDs   []string
	ValidFrom  time.Time
}

// Fact 是有来源、有观测时间的共享事实。
type Fact struct {
	Key      string
	Value    string
	Channel  event.Channel
	Session  string
	Private  bool
	SourceID string
	At       time.Time
}

// Context 是已按范围召回的数据快照。
//
// Recent 是房间现场；Focus 是按 reply_to 证据配对的双方对话轮次，
// 有独立窗口，不会被热闹房间的 Recent 挤掉。
type Context struct {
	Scope        Scope
	Recent       []Message
	Focus        []Message
	CrossChannel []Message
	Records      []Record
}

// SharedState 是当前范围可见的事实快照。
type SharedState struct {
	Scope Scope
	Facts []Fact
}

// Reader 的实际查询留0.9；失败不得伪装成空数据。
type Reader interface {
	Recall(context.Context, Scope) (Context, error)
	Shared(context.Context, Scope) (SharedState, error)
}

// ValidateScope 校验目标通道和私密性，错误不包含正文。
func ValidateScope(scope Scope) error {
	if !scope.Channel.Valid() || strings.TrimSpace(scope.Session) == "" {
		return errors.New("记忆目标范围无效")
	}
	if (scope.Channel == event.ChLive || scope.Channel == event.ChComment) && scope.Private {
		return errors.New("公开通道不能声明私密目标")
	}
	if scope.Channel == event.ChDM && !scope.Private {
		return errors.New("DM目标必须为私密")
	}
	return nil
}

func allowed(scope Scope, ch event.Channel, session string, private bool) bool {
	if ch == event.ChDM && !private {
		return false
	}
	return !private || (scope.Private && ch == scope.Channel && session == scope.Session)
}
func validMessage(m Message) bool {
	return strings.TrimSpace(m.EventID) != "" && m.Channel.Valid() && strings.TrimSpace(m.Session) != "" && m.Dir.Valid() && strings.TrimSpace(m.Text) != "" && !m.At.IsZero()
}
func score(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1 }

// Validate 拒绝错会话和私密越界；不替代repository的查询过滤。
func (c Context) Validate() error {
	if err := ValidateScope(c.Scope); err != nil {
		return err
	}
	for _, m := range c.Recent {
		if !validMessage(m) || m.Channel != c.Scope.Channel || m.Session != c.Scope.Session || !allowed(c.Scope, m.Channel, m.Session, m.Private) {
			return errors.New("Recent消息无效或越过目标范围")
		}
	}
	for _, m := range c.Focus {
		if !validMessage(m) || m.Channel != c.Scope.Channel || m.Session != c.Scope.Session || !allowed(c.Scope, m.Channel, m.Session, m.Private) {
			return errors.New("Focus消息无效或越过目标范围")
		}
	}
	for _, m := range c.CrossChannel {
		if !validMessage(m) || m.Channel == c.Scope.Channel || m.Private || !allowed(c.Scope, m.Channel, m.Session, m.Private) {
			return errors.New("CrossChannel只允许其他通道公开消息")
		}
	}
	for _, r := range c.Records {
		if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Summary) == "" || !score(r.Importance) || !score(r.Confidence) {
			return errors.New("记忆摘要或评分无效")
		}
		if r.Channel != "" && !r.Channel.Valid() {
			return errors.New("记忆来源通道无效")
		}
		if !allowed(c.Scope, r.Channel, r.Session, r.Private) {
			return errors.New("记忆摘要越过私密范围")
		}
	}
	return nil
}

// Validate 拒绝无来源、无时间或跨私密会话事实。
func (s SharedState) Validate() error {
	if err := ValidateScope(s.Scope); err != nil {
		return err
	}
	for _, f := range s.Facts {
		if strings.TrimSpace(f.Key) == "" || strings.TrimSpace(f.Value) == "" || strings.TrimSpace(f.SourceID) == "" || f.At.IsZero() || !f.Channel.Valid() || strings.TrimSpace(f.Session) == "" {
			return errors.New("共享事实缺少有效来源或观测信息")
		}
		if !allowed(s.Scope, f.Channel, f.Session, f.Private) {
			return errors.New("共享事实越过私密范围")
		}
	}
	return nil
}
