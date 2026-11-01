package memory

// 本文件测 0.9c 的召回装配：窗口控制、错误透传、越界数据二次校验。
// 全部用假 RecordSource，不连 PG（真实 PG 的组合用例在 internal/database）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

var readerBaseTime = time.Date(2026, 11, 1, 19, 0, 0, 0, time.UTC)

// fakeRecordSource 是受控的三段查询实现，记录实际收到的 limit。
type fakeRecordSource struct {
	recent  []Message
	cross   []Message
	records []Record

	failOn string // recent / cross / records
	err    error

	gotRecent, gotCross, gotRecords int
	calls                           int
}

var errFake = errors.New("查询炸了")

func (f *fakeRecordSource) Recent(_ context.Context, _ Scope, limit int) ([]Message, error) {
	f.calls++
	f.gotRecent = limit
	if f.failOn == "recent" {
		return nil, f.err
	}
	return f.recent, nil
}

func (f *fakeRecordSource) CrossChannel(_ context.Context, _ Scope, limit int) ([]Message, error) {
	f.calls++
	f.gotCross = limit
	if f.failOn == "cross" {
		return nil, f.err
	}
	return f.cross, nil
}

func (f *fakeRecordSource) Records(_ context.Context, _ Scope, limit int) ([]Record, error) {
	f.calls++
	f.gotRecords = limit
	if f.failOn == "records" {
		return nil, f.err
	}
	return f.records, nil
}

// fakeFactSource 只实现 FactSource，用来验证 Shared 的可选注入。
type fakeFactSource struct {
	facts    []Fact
	failWith error
	gotLimit int
}

func (f *fakeFactSource) Facts(_ context.Context, _ Scope, limit int) ([]Fact, error) {
	f.gotLimit = limit
	if f.failWith != nil {
		return nil, f.failWith
	}
	return f.facts, nil
}

// liveScope 是公开目标范围。
func liveScope() Scope { return Scope{Channel: "live", Session: "room-1"} }

// dmScope 是私密目标范围。
func dmScope() Scope { return Scope{Channel: "dm", Session: "u-9", Private: true} }

// publicMessage 造一条合法公开消息。
func publicMessage(id string, ch event.Channel, session string) Message {
	return Message{EventID: id, Channel: ch, Session: session, Dir: "in", Text: "说话", At: readerBaseTime}
}

// TestReaderRecallAssembles 正常装配：三段数据都进入 Context，且 limit 按配置下发。
func TestReaderRecallAssembles(t *testing.T) {
	src := &fakeRecordSource{
		recent:  []Message{publicMessage("live:1:1", "live", "room-1")},
		cross:   []Message{publicMessage("comment:2:1", "comment", "c-1")},
		records: []Record{{ID: "mem_1", Summary: "摘要", Importance: 0.5, Confidence: 0.5, ValidFrom: readerBaseTime}},
	}

	r, err := NewReader(src, nil, ReaderOptions{RecentLimit: 5, CrossLimit: 3, RecordsLimit: 2})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	got, err := r.Recall(context.Background(), liveScope())
	if err != nil {
		t.Fatalf("Recall 失败：%v", err)
	}
	if len(got.Recent) != 1 || len(got.CrossChannel) != 1 || len(got.Records) != 1 {
		t.Errorf("装配结果不符：%+v", got)
	}
	if got.Scope != liveScope() {
		t.Errorf("Scope = %+v，期望目标范围", got.Scope)
	}
	if src.gotRecent != 5 || src.gotCross != 3 || src.gotRecords != 2 {
		t.Errorf("limit 未按配置下发：recent=%d cross=%d records=%d", src.gotRecent, src.gotCross, src.gotRecords)
	}
	if src.calls != 3 {
		t.Errorf("查询次数 = %d，期望 3", src.calls)
	}
}

// TestReaderDefaultAndClampLimits 零值用默认值，越界夹到上限。
func TestReaderDefaultAndClampLimits(t *testing.T) {
	r, err := NewReader(&fakeRecordSource{}, nil, ReaderOptions{})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	limits := r.Limits()
	if limits.RecentLimit != defaultRecentLimit || limits.CrossLimit != defaultCrossLimit ||
		limits.RecordsLimit != defaultRecordsLimit || limits.FactsLimit != defaultFactsLimit {
		t.Errorf("默认窗口不符：%+v", limits)
	}

	r2, err := NewReader(&fakeRecordSource{}, nil, ReaderOptions{RecentLimit: 999, CrossLimit: -3, RecordsLimit: 200, FactsLimit: 1})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	l2 := r2.Limits()
	if l2.RecentLimit != maxReaderLimit {
		t.Errorf("RecentLimit = %d，期望夹到 %d", l2.RecentLimit, maxReaderLimit)
	}
	if l2.CrossLimit != defaultCrossLimit {
		t.Errorf("负数 CrossLimit = %d，期望回落默认 %d", l2.CrossLimit, defaultCrossLimit)
	}
	if l2.RecordsLimit != maxReaderLimit {
		t.Errorf("RecordsLimit = %d，期望夹到 %d", l2.RecordsLimit, maxReaderLimit)
	}
	if l2.FactsLimit != 1 {
		t.Errorf("FactsLimit = %d，期望保留 1", l2.FactsLimit)
	}
}

// TestReaderRejectsInvalidScope 非法范围在查询前就被挡住。
func TestReaderRejectsInvalidScope(t *testing.T) {
	src := &fakeRecordSource{}
	r, _ := NewReader(src, nil, ReaderOptions{})

	bad := []Scope{
		{}, // 零值
		{Channel: "live", Session: "", Private: false},      // 缺 session
		{Channel: "live", Session: "r", Private: true},      // 公开通道声明私密
		{Channel: "dm", Session: "u", Private: false},       // dm 未声明私密
		{Channel: "telegram", Session: "r", Private: false}, // 未知通道
	}
	for _, scope := range bad {
		if _, err := r.Recall(context.Background(), scope); err == nil {
			t.Errorf("范围 %+v 应报错", scope)
		}
	}
	if src.calls != 0 {
		t.Errorf("非法范围不应触发查询，实际调用 %d 次", src.calls)
	}
}

// TestReaderPropagatesSourceError 三段任一失败都必须报错，不能当空数据吞掉。
func TestReaderPropagatesSourceError(t *testing.T) {
	for _, stage := range []string{"recent", "cross", "records"} {
		t.Run(stage, func(t *testing.T) {
			src := &fakeRecordSource{failOn: stage, err: errFake}
			r, _ := NewReader(src, nil, ReaderOptions{})
			if _, err := r.Recall(context.Background(), liveScope()); !errors.Is(err, errFake) {
				t.Fatalf("错误未透传：%v", err)
			}
		})
	}
}

// TestReaderCanceledContext ctx 取消要保留 context.Canceled。
func TestReaderCanceledContext(t *testing.T) {
	src := &fakeRecordSource{}
	r, _ := NewReader(src, nil, ReaderOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.Recall(ctx, liveScope()); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，实际 %v", err)
	}
	if _, err := r.Shared(ctx, liveScope()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shared 期望 context.Canceled，实际 %v", err)
	}
	if src.calls != 0 {
		t.Errorf("取消后不应查询，实际 %d 次", src.calls)
	}
}

// TestReaderRejectsPrivateLeak 公开范围拿到私密记录 → 二次校验必须拦下。
func TestReaderRejectsPrivateLeak(t *testing.T) {
	src := &fakeRecordSource{
		records: []Record{{
			ID: "mem_secret", Summary: "私密摘要", Importance: 0.5, Confidence: 0.5,
			Private: true, Channel: "dm", Session: "u-9", ValidFrom: readerBaseTime,
		}},
	}
	r, _ := NewReader(src, nil, ReaderOptions{})
	if _, err := r.Recall(context.Background(), liveScope()); err == nil {
		t.Fatal("私密记录混入公开范围时必须报错")
	}
}

// TestReaderRejectsCrossChannelLeak CrossChannel 混进同通道消息 → 校验拦下。
func TestReaderRejectsCrossChannelLeak(t *testing.T) {
	src := &fakeRecordSource{
		cross: []Message{publicMessage("live:1:2", "live", "room-1")}, // 同通道，非法
	}
	r, _ := NewReader(src, nil, ReaderOptions{})
	if _, err := r.Recall(context.Background(), liveScope()); err == nil {
		t.Fatal("CrossChannel 混入同通道消息时必须报错")
	}
}

// TestReaderSharedWithoutFactSource 没注入事实源 → 显式空 Facts，Scope 正确。
func TestReaderSharedWithoutFactSource(t *testing.T) {
	r, _ := NewReader(&fakeRecordSource{}, nil, ReaderOptions{})
	got, err := r.Shared(context.Background(), dmScope())
	if err != nil {
		t.Fatalf("Shared 失败：%v", err)
	}
	if got.Scope != dmScope() {
		t.Errorf("Scope = %+v，期望目标范围", got.Scope)
	}
	if got.Facts != nil {
		t.Errorf("Facts = %v，期望 nil（没有事实来源）", got.Facts)
	}
}

// TestReaderSharedWithFactSource 注入事实源后正常返回，并把 limit 透传下去。
func TestReaderSharedWithFactSource(t *testing.T) {
	facts := []Fact{{
		Key: "存粮", Value: "未知", Channel: "live", Session: "room-1",
		SourceID: "probe:1", At: readerBaseTime,
	}}
	fs := &fakeFactSource{facts: facts}
	r, err := NewReader(&fakeRecordSource{}, fs, ReaderOptions{FactsLimit: 7})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	got, err := r.Shared(context.Background(), liveScope())
	if err != nil {
		t.Fatalf("Shared 失败：%v", err)
	}
	if len(got.Facts) != 1 || got.Facts[0].Key != "存粮" {
		t.Errorf("Facts = %+v", got.Facts)
	}
	if fs.gotLimit != 7 {
		t.Errorf("FactsLimit 未透传：%d", fs.gotLimit)
	}

	// 事实越界（私密事实进入公开范围）必须报错
	fsBad := &fakeFactSource{facts: []Fact{{
		Key: "k", Value: "v", Channel: "dm", Session: "u-9", Private: true,
		SourceID: "s", At: readerBaseTime,
	}}}
	rBad, _ := NewReader(&fakeRecordSource{}, fsBad, ReaderOptions{})
	if _, err := rBad.Shared(context.Background(), liveScope()); err == nil {
		t.Fatal("私密事实进入公开范围时必须报错")
	}

	// 事实查询失败必须透传
	fsErr := &fakeFactSource{failWith: errFake}
	rErr, _ := NewReader(&fakeRecordSource{}, fsErr, ReaderOptions{})
	if _, err := rErr.Shared(context.Background(), liveScope()); !errors.Is(err, errFake) {
		t.Fatalf("错误未透传：%v", err)
	}
}

// TestNewReaderRejectsNil 缺查询来源要在构造期失败。
func TestNewReaderRejectsNil(t *testing.T) {
	if _, err := NewReader(nil, nil, ReaderOptions{}); err == nil {
		t.Error("source 为 nil 应报错")
	}
}
