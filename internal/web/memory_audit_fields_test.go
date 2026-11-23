package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

// TestMemoryAPIShowsProvenanceAndUsage M8：面板记忆行必须能回答
// 「从哪来、什么时候有效、可不可信、被引用过几次」。
func TestMemoryAPIShowsProvenanceAndUsage(t *testing.T) {
	invalid := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	repo := &fakeRepo{memory: []store.WebMemory{{
		ID: "mem-1", Kind: "fact", Title: "住在城东", Summary: "用户住在城东",
		Importance: 0.8, Confidence: 0.9, Source: "owner_direct",
		SourceEventID: "dm:1:2:3", ValidFrom: time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC),
		ValidTo: &invalid, RefCount: 7, CreatedAt: time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC),
	}}}
	srv := newTestServer(t, repo)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/memory?all=1"))
	if rec.Code != 200 {
		t.Fatalf("状态码=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("条数=%d", len(body.Items))
	}
	item := body.Items[0]
	if item["source"] != "owner_direct" || item["source_event_id"] != "dm:1:2:3" {
		t.Fatalf("来源字段错误：%+v", item)
	}
	if item["ref_count"] != float64(7) || item["confidence"] != 0.9 {
		t.Fatalf("引用/置信字段错误：%+v", item)
	}
	if item["valid_to"] != invalid.Format(time.RFC3339) {
		t.Fatalf("valid_to=%v", item["valid_to"])
	}
}

// TestMemoryPageShowsProvenanceColumns 页面必须渲染来源/版本/引用。
func TestMemoryPageShowsProvenanceColumns(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/memory"))
	body := rec.Body.String()
	for _, want := range []string{
		"来源 / 版本", "重要度 / 置信 / 引用", "valid_from", "ref_count",
		"显示已失效/已归档", "source_event_id",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("记忆页缺少 %q", want)
		}
	}
}
