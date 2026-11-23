// Package trace 记录单事件的分阶段耗时；只存元数据，不存 prompt/正文/凭据。
package trace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Attr 是单个 span 属性；值只允许 ID/长度/枚举/数值等元数据。
type Attr struct {
	Key   string
	Value any
}

func A(key string, value any) Attr { return Attr{Key: key, Value: value} }

// Span 是一个阶段；Start/End 之外的字段都可在结束前通过 Add 更新。
type Span struct {
	TraceID    string         `json:"trace_id"`
	SpanID     string         `json:"span_id"`
	ParentID   string         `json:"parent_id,omitempty"`
	Name       string         `json:"name"`
	Start      time.Time      `json:"start"`
	End        time.Time      `json:"end,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Error      string         `json:"error,omitempty"`

	rec   *Local
	ended bool
}

// Recorder 是上下文式记录器；nil span / 无 recorder 时所有操作都是 no-op。
type Recorder interface {
	Start(context.Context, string, ...Attr) (context.Context, *Span)
	End(*Span, error)
}

type ctxKey struct{}

type traceState struct {
	rec   Recorder
	span  *Span
	trace string
}

// WithRecorder 把记录器注入上下文；重复注入以最外层为准。
func WithRecorder(ctx context.Context, rec Recorder) context.Context {
	if ctx == nil || rec == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, &traceState{rec: rec})
}

// Start 在当前 span 下创建子 span；没有 recorder 时返回 no-op。
func Start(ctx context.Context, name string, attrs ...Attr) (context.Context, *Span) {
	if ctx == nil {
		return ctx, nil
	}
	state, _ := ctx.Value(ctxKey{}).(*traceState)
	if state == nil || state.rec == nil {
		return ctx, nil
	}
	return state.rec.Start(ctx, name, attrs...)
}

// End 结束 span；nil 安全。
func End(span *Span, err error) {
	if span == nil || span.rec == nil {
		return
	}
	span.rec.End(span, err)
}

// Add 给当前 span 追加属性；nil 安全。
func Add(ctx context.Context, attrs ...Attr) {
	if ctx == nil || len(attrs) == 0 {
		return
	}
	state, _ := ctx.Value(ctxKey{}).(*traceState)
	if state == nil || state.span == nil {
		return
	}
	state.span.attach(attrs...)
}

func (s *Span) attach(attrs ...Attr) {
	if s == nil || len(attrs) == 0 || s.ended {
		return
	}
	if s.Attributes == nil {
		s.Attributes = make(map[string]any, len(attrs))
	}
	for _, attr := range attrs {
		if attr.Key == "" {
			continue
		}
		s.Attributes[attr.Key] = attr.Value
	}
}

// Local 是进程内环形缓冲 + 可选 JSONL 落盘。
type Local struct {
	mu    sync.Mutex
	max   int
	spans []*Span
	file  *jsonlWriter
	now   func() time.Time
}

// NewLocal 创建本地 trace；max=0 用 5000，path 为空不落文件。
func NewLocal(max int, path string) (*Local, error) {
	if max == 0 {
		max = 5000
	}
	if max < 100 || max > 200000 {
		return nil, fmt.Errorf("trace 容量需在100至200000")
	}
	l := &Local{max: max, now: time.Now}
	if path != "" {
		writer, err := newJSONLWriter(path)
		if err != nil {
			return nil, err
		}
		l.file = writer
	}
	return l, nil
}

// StartEvent 以事件 ID 派生稳定 trace，并生成阶段链；
// 即使 collect/schedule/generate 在不同进程/不同请求处理，也会落到同一 trace。
func (l *Local) StartEvent(ctx context.Context, eventID, stage string, attrs ...Attr) (context.Context, *Span) {
	if l == nil || ctx == nil || eventID == "" {
		return ctx, nil
	}
	traceID := TraceID(eventID)
	spanID := traceID + "-" + stage
	parentID := ""
	if prev, ok := previousStage(stage); ok {
		parentID = traceID + "-" + prev
	}
	span := &Span{TraceID: traceID, SpanID: spanID, ParentID: parentID, Name: stage, Start: l.now().UTC(), rec: l}
	span.attach(A("event_id", eventID))
	span.attach(attrs...)
	return context.WithValue(ctx, ctxKey{}, &traceState{rec: l, span: span, trace: traceID}), span
}

func (l *Local) Start(ctx context.Context, name string, attrs ...Attr) (context.Context, *Span) {
	if l == nil || ctx == nil {
		return ctx, nil
	}
	parent, _ := ctx.Value(ctxKey{}).(*traceState)
	traceID := ""
	parentID := ""
	if parent != nil {
		traceID = parent.trace
		if parent.span != nil {
			parentID = parent.span.SpanID
		}
	}
	if traceID == "" {
		traceID = TraceID(name)
	}
	span := &Span{TraceID: traceID, SpanID: newSpanID(traceID), ParentID: parentID, Name: name, Start: l.now().UTC(), rec: l}
	span.attach(attrs...)
	state := &traceState{rec: l, span: span, trace: traceID}
	return context.WithValue(ctx, ctxKey{}, state), span
}

func (l *Local) End(span *Span, err error) {
	if l == nil || span == nil {
		return
	}
	l.mu.Lock()
	if span.ended {
		l.mu.Unlock()
		return
	}
	span.ended = true
	span.End = l.now().UTC()
	if err != nil {
		span.Error = err.Error()
	}
	l.spans = append(l.spans, span)
	if len(l.spans) > l.max {
		l.spans = l.spans[len(l.spans)-l.max:]
	}
	writer := l.file
	data := spanJSON(span)
	l.mu.Unlock()
	if writer != nil {
		writer.write(data)
	}
}

// Close 关闭 JSONL 文件；重复调用安全。
func (l *Local) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.close()
}

// TraceID 由事件 ID 稳定派生，便于跨阶段聚合；不暴露原始事件 ID。
func TraceID(eventID string) string {
	sum := sha256.Sum256([]byte(eventID))
	return hex.EncodeToString(sum[:8])
}

func newSpanID(traceID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", traceID, time.Now().UnixNano())))
	return hex.EncodeToString(sum[:8])
}

var stageOrder = map[string]int{"collect": 0, "schedule": 1, "generate": 2, "deliver": 3, "verify": 4}

func previousStage(stage string) (string, bool) {
	order, ok := stageOrder[stage]
	if !ok || order == 0 {
		return "", false
	}
	for name, n := range stageOrder {
		if n == order-1 {
			return name, true
		}
	}
	return "", false
}

// TraceSummary 是面板列表项。
type TraceSummary struct {
	TraceID    string    `json:"trace_id"`
	EventID    string    `json:"event_id"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	DurationMS int64     `json:"duration_ms"`
	SpanCount  int       `json:"span_count"`
	HasError   bool      `json:"has_error"`
}

// TraceDetail 是单条 trace 的完整 span 列表。
type TraceDetail struct {
	TraceSummary
	Spans []Span `json:"spans"`
}

// Recent 按开始时间倒序返回最近 trace；slow=true 按总耗时倒序。
func (l *Local) Recent(limit int, slow bool) []TraceSummary {
	if l == nil || limit < 1 || limit > 200 {
		return nil
	}
	l.mu.Lock()
	spans := append([]*Span(nil), l.spans...)
	l.mu.Unlock()
	grouped := groupSpans(spans)
	out := make([]TraceSummary, 0, len(grouped))
	for _, g := range grouped {
		out = append(out, summarize(g))
	}
	sort.Slice(out, func(i, j int) bool {
		if slow {
			return out[i].DurationMS > out[j].DurationMS
		}
		return out[i].Start.After(out[j].Start)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Get 返回单个 trace；不存在时返回空 span 列表。
func (l *Local) Get(traceID string) TraceDetail {
	if l == nil || traceID == "" {
		return TraceDetail{}
	}
	l.mu.Lock()
	var picked []*Span
	for _, span := range l.spans {
		if span.TraceID == traceID {
			picked = append(picked, span)
		}
	}
	l.mu.Unlock()
	if len(picked) == 0 {
		return TraceDetail{TraceSummary: TraceSummary{TraceID: traceID}}
	}
	summary := summarize(picked)
	detail := TraceDetail{TraceSummary: summary, Spans: make([]Span, 0, len(picked))}
	sort.Slice(picked, func(i, j int) bool { return picked[i].Start.Before(picked[j].Start) })
	for _, span := range picked {
		copy := *span
		copy.rec = nil
		detail.Spans = append(detail.Spans, copy)
	}
	return detail
}

func groupSpans(spans []*Span) map[string][]*Span {
	out := make(map[string][]*Span)
	for _, span := range spans {
		out[span.TraceID] = append(out[span.TraceID], span)
	}
	return out
}

func summarize(spans []*Span) TraceSummary {
	if len(spans) == 0 {
		return TraceSummary{}
	}
	summary := TraceSummary{TraceID: spans[0].TraceID, Start: spans[0].Start, End: spans[0].End, SpanCount: len(spans)}
	for _, span := range spans {
		if summary.EventID == "" {
			if v, ok := span.Attributes["event_id"].(string); ok {
				summary.EventID = v
			}
		}
		if span.Start.Before(summary.Start) {
			summary.Start = span.Start
		}
		if span.End.After(summary.End) {
			summary.End = span.End
		}
		if span.Error != "" {
			summary.HasError = true
		}
	}
	if !summary.End.IsZero() {
		summary.DurationMS = summary.End.Sub(summary.Start).Milliseconds()
	}
	return summary
}
