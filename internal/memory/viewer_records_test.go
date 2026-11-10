package memory

// 本文件测观众相关记忆的可选能力：
// 装配层校验与转发、数据二次校验、HybridSearcher 透传、底层不支持时返回空。

import (
	"context"
	"errors"
	"testing"

	"RaiseMyFish/internal/event"
)

var viewerRecordsScope = Scope{Channel: event.ChLive, Session: "room"}

// fakeViewerSource 在既有三段查询上多实现 ViewerRecords。
type fakeViewerSource struct {
	fakeRecordSource

	records []Record
	err     error

	viewerCalls int
	gotViewer   ViewerRecordQuery
}

func (f *fakeViewerSource) ViewerRecords(_ context.Context, q ViewerRecordQuery) ([]Record, error) {
	f.viewerCalls++
	f.gotViewer = q
	if f.err != nil {
		return nil, f.err
	}
	return f.records, nil
}

func viewerRecordsFixture() Record {
	return Record{
		ID: "vm-1", Channel: event.ChLive, Session: "room", Summary: "昨晚顶号了",
		Importance: 0.6, Confidence: 0.9, ValidFrom: readerBaseTime,
	}
}

// TestAssembledReaderViewerRecordsForwards UID 去空格、limit 夹取后原样转发。
func TestAssembledReaderViewerRecordsForwards(t *testing.T) {
	source := &fakeViewerSource{records: []Record{viewerRecordsFixture()}}
	reader, err := NewReader(source, nil, ReaderOptions{})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := reader.ViewerRecords(context.Background(), ViewerRecordQuery{
		Scope: viewerRecordsScope, UID: " 42 ", Limit: 0,
	})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if source.viewerCalls != 1 || source.gotViewer.UID != "42" || source.gotViewer.Limit != defaultRecordsLimit || source.gotViewer.Scope != viewerRecordsScope {
		t.Fatalf("未按约定转发：calls=%d query=%+v", source.viewerCalls, source.gotViewer)
	}
	if len(got) != 1 || got[0].ID != "vm-1" {
		t.Fatalf("返回结果不符：%+v", got)
	}

	if _, err := reader.ViewerRecords(context.Background(), ViewerRecordQuery{
		Scope: viewerRecordsScope, UID: "42", Limit: maxReaderLimit + 50,
	}); err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if source.gotViewer.Limit != maxReaderLimit {
		t.Fatalf("limit 未夹取：%d", source.gotViewer.Limit)
	}
}

// TestAssembledReaderViewerRecordsOptional 来源未实现能力时返回空，不报错。
func TestAssembledReaderViewerRecordsOptional(t *testing.T) {
	reader, err := NewReader(&fakeRecordSource{}, nil, ReaderOptions{})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	got, err := reader.ViewerRecords(context.Background(), ViewerRecordQuery{Scope: viewerRecordsScope, UID: "42", Limit: 5})
	if err != nil || got != nil {
		t.Fatalf("无能力时应返回 nil,nil：got=%+v err=%v", got, err)
	}
}

// TestAssembledReaderViewerRecordsRejects 参数与数据越界在装配层拒绝。
func TestAssembledReaderViewerRecordsRejects(t *testing.T) {
	bad := viewerRecordsFixture()
	bad.Summary = ""
	private := viewerRecordsFixture()
	private.ID = "vm-private"
	private.Private = true
	private.Session = "dm"
	private.Channel = event.ChDM

	cases := []struct {
		name    string
		source  *fakeViewerSource
		ctx     context.Context
		query   ViewerRecordQuery
		wantErr bool
	}{
		{"nil ctx", &fakeViewerSource{}, nil, ViewerRecordQuery{Scope: viewerRecordsScope, UID: "42"}, true},
		{"空 UID", &fakeViewerSource{}, context.Background(), ViewerRecordQuery{Scope: viewerRecordsScope, UID: "  "}, true},
		{"非法范围", &fakeViewerSource{}, context.Background(), ViewerRecordQuery{Scope: Scope{Channel: event.ChLive}, UID: "42"}, true},
		{"来源失败", &fakeViewerSource{err: errFake}, context.Background(), ViewerRecordQuery{Scope: viewerRecordsScope, UID: "42"}, true},
		{"摘要为空", &fakeViewerSource{records: []Record{bad}}, context.Background(), ViewerRecordQuery{Scope: viewerRecordsScope, UID: "42"}, true},
		{"私密越界", &fakeViewerSource{records: []Record{private}}, context.Background(), ViewerRecordQuery{Scope: viewerRecordsScope, UID: "42"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader, err := NewReader(tc.source, nil, ReaderOptions{})
			if err != nil {
				t.Fatalf("装配失败：%v", err)
			}
			//nolint:staticcheck // 刻意传 nil ctx 验证防御边界
			_, gotErr := reader.ViewerRecords(tc.ctx, tc.query)
			if (gotErr != nil) != tc.wantErr {
				t.Fatalf("err = %v，期望有错=%v", gotErr, tc.wantErr)
			}
			if tc.name == "来源失败" && !errors.Is(gotErr, errFake) {
				t.Fatalf("来源错误未保留原因：%v", gotErr)
			}
		})
	}
}

// TestHybridSearcherForwardsViewerRecords 转发到底层 Reader；不支持时返回空。
func TestHybridSearcherForwardsViewerRecords(t *testing.T) {
	source := &fakeViewerSource{records: []Record{viewerRecordsFixture()}}
	base, err := NewReader(source, nil, ReaderOptions{})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	h, err := NewHybridSearcher(base, &fakeSearchSource{}, nil, HybridSearchOptions{Alpha: 0.5})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	got, err := h.ViewerRecords(context.Background(), ViewerRecordQuery{Scope: viewerRecordsScope, UID: "42", Limit: 5})
	if err != nil || len(got) != 1 || source.viewerCalls != 1 {
		t.Fatalf("未转发观众相关记忆：got=%+v calls=%d err=%v", got, source.viewerCalls, err)
	}

	plain, err := NewHybridSearcher(&fakeSearchBase{}, &fakeSearchSource{}, nil, HybridSearchOptions{Alpha: 0.5})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if got, err := plain.ViewerRecords(context.Background(), ViewerRecordQuery{Scope: viewerRecordsScope, UID: "42"}); err != nil || got != nil {
		t.Fatalf("底层不支持时应返回 nil,nil：got=%+v err=%v", got, err)
	}
}
