package memory

// 本文件测 0.10 时间窗召回：走 Between 两路、跳过语义、空关键词与非法窗口。

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakeWindowSource 在既有检索源上实现「关键词 + 时间窗」两路。
type fakeWindowSource struct {
	fakeSearchSource

	windowRecords     []Record
	windowMessages    []Message
	recordErr         error
	messageErr        error
	gotAfter          time.Time
	gotBefore         time.Time
	gotWindowKeyword  string
	gotWindowMsgTerm  string
	recordWindowCalls int
	msgWindowCalls    int
	gotMsgAfter       time.Time
	gotMsgBefore      time.Time
}

func (f *fakeWindowSource) KeywordRecordsBetween(_ context.Context, _ Scope, keyword string, after, before time.Time, limit int) ([]Record, error) {
	f.recordWindowCalls++
	f.gotWindowKeyword = keyword
	f.gotAfter, f.gotBefore = after, before
	f.gotLimit = limit
	return f.windowRecords, f.recordErr
}

func (f *fakeWindowSource) KeywordMessagesBetween(_ context.Context, _ Scope, keyword string, after, before time.Time, limit int) ([]Message, error) {
	f.msgWindowCalls++
	f.gotWindowMsgTerm = keyword
	f.gotMsgAfter, f.gotMsgBefore = after, before
	return f.windowMessages, f.messageErr
}

func windowRange() (time.Time, time.Time) {
	after := time.Date(2026, 11, 9, 0, 0, 0, 0, time.Local)
	return after, after.Add(24 * time.Hour)
}

// TestHybridSearchWindowUsesBetweenSource 有时间窗时只走时间窗两路，不消耗 embedding。
func TestHybridSearchWindowUsesBetweenSource(t *testing.T) {
	source := &fakeWindowSource{
		windowRecords:  []Record{searchRecord("win-1", "昨天聊到白饭", 0.5)},
		windowMessages: []Message{{EventID: "ev-1", Channel: "live", Session: "room-1", Dir: "in", Text: "昨天聊到白饭", At: readerBaseTime}},
	}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(1)}}
	h := newTestHybrid(t, source, embedder, HybridSearchOptions{Alpha: 0.5})
	after, before := windowRange()

	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "聊天", After: after, Before: before})
	if err != nil {
		t.Fatalf("时间窗检索失败：%v", err)
	}
	if source.recordWindowCalls != 1 || source.msgWindowCalls != 1 {
		t.Fatalf("时间窗调用次数不符：records=%d messages=%d", source.recordWindowCalls, source.msgWindowCalls)
	}
	if source.keywordCalls != 0 || source.semanticCalls != 0 || source.messageCalls != 0 || embedder.calls != 0 {
		t.Errorf("时间窗不应走旧三路：kw=%d sem=%d msg=%d embed=%d",
			source.keywordCalls, source.semanticCalls, source.messageCalls, embedder.calls)
	}
	if !source.gotAfter.Equal(after) || !source.gotBefore.Equal(before) {
		t.Errorf("摘要时间窗未透传：%v ~ %v", source.gotAfter, source.gotBefore)
	}
	if !source.gotMsgAfter.Equal(after) || !source.gotMsgBefore.Equal(before) {
		t.Errorf("消息时间窗未透传：%v ~ %v", source.gotMsgAfter, source.gotMsgBefore)
	}
	if source.gotWindowKeyword != "聊天" || source.gotWindowMsgTerm != "聊天" {
		t.Errorf("关键词未透传：records=%q messages=%q", source.gotWindowKeyword, source.gotWindowMsgTerm)
	}
	if len(got.Records) != 1 || got.Records[0].ID != "win-1" || len(got.Recent) != 1 {
		t.Fatalf("时间窗结果不符：%+v", got)
	}
}

// TestHybridSearchWindowAllowsEmptyKeyword 「昨天说过什么」允许只按时间窗召回。
func TestHybridSearchWindowAllowsEmptyKeyword(t *testing.T) {
	source := &fakeWindowSource{windowRecords: []Record{searchRecord("win-1", "昨晚的事", 0.5)}}
	h := newTestHybrid(t, source, nil, HybridSearchOptions{})
	after, before := windowRange()

	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "  ", After: after, Before: before})
	if err != nil {
		t.Fatalf("空关键词时间窗失败：%v", err)
	}
	if source.recordWindowCalls != 1 || source.gotWindowKeyword != "" {
		t.Errorf("应只按时间窗检索：calls=%d keyword=%q", source.recordWindowCalls, source.gotWindowKeyword)
	}
	if len(got.Records) != 1 {
		t.Fatalf("结果不符：%+v", got)
	}
}

// TestHybridSearchWindowRejectsInvalidRange 起点不早于终点直接拒绝。
func TestHybridSearchWindowRejectsInvalidRange(t *testing.T) {
	h := newTestHybrid(t, &fakeWindowSource{}, nil, HybridSearchOptions{})
	after, before := windowRange()
	_, err := h.Search(context.Background(), SearchQuery{
		Scope: searchScope(), Text: "聊天", After: before, Before: after,
	})
	if err == nil || !strings.Contains(err.Error(), "时间窗") {
		t.Fatalf("非法窗口应报错：%v", err)
	}
}

// TestHybridSearchWindowRequiresSource 数据源不支持时间窗时明确报错，
// 不能把无时间过滤的旧结果伪装成「昨天」。
func TestHybridSearchWindowRequiresSource(t *testing.T) {
	h := newTestHybrid(t, &fakeSearchSource{}, nil, HybridSearchOptions{})
	after, before := windowRange()
	_, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "聊天", After: after, Before: before})
	if err == nil || !strings.Contains(err.Error(), "不支持时间窗") {
		t.Fatalf("缺时间窗源应报错：%v", err)
	}
}

// TestHybridSearchWindowRecordsOnly 消息源不支持时间窗时仍返回摘要结果。
func TestHybridSearchWindowRecordsOnly(t *testing.T) {
	source := &recordsOnlyWindowSource{
		fakeSearchSource: fakeSearchSource{},
		records:          []Record{searchRecord("win-1", "昨天的事", 0.5)},
	}
	h := newTestHybrid(t, source, nil, HybridSearchOptions{})
	after, before := windowRange()

	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "顶号", After: after, Before: before})
	if err != nil {
		t.Fatalf("仅有摘要源时失败：%v", err)
	}
	if len(got.Records) != 1 || len(got.Recent) != 0 {
		t.Fatalf("结果不符：%+v", got)
	}
}

// recordsOnlyWindowSource 只实现摘要时间窗接口，不实现 KeywordMessagesBetween。
type recordsOnlyWindowSource struct {
	fakeSearchSource
	records []Record
}

func (f *recordsOnlyWindowSource) KeywordRecordsBetween(context.Context, Scope, string, time.Time, time.Time, int) ([]Record, error) {
	return f.records, nil
}
