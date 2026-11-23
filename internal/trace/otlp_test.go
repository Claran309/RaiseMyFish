package trace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOTLPExporterPayload OTLP/HTTP JSON 载荷的 ID 长度、服务名、属性与错误状态。
func TestOTLPExporterPayload(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Method != http.MethodPost {
			t.Errorf("路径/方法错误：%s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("Content-Type=%q", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解码载荷：%v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	exporter, err := NewOTLPExporter(server.URL, "fishd-test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1760000000, 0).UTC()
	err = exporter.Export(context.Background(), []Span{{
		TraceID: "0011223344556677", SpanID: "8899aabbccddeeff", ParentID: "0011223344556677",
		Name: "llm.step", Start: start, End: start.Add(150 * time.Millisecond),
		Attributes: map[string]any{"channel": "comment", "steps": 2, "ok": true},
		Error:      "预算不足",
	}})
	if err != nil {
		t.Fatal(err)
	}
	resourceSpans := got["resourceSpans"].([]any)
	resource := resourceSpans[0].(map[string]any)["resource"].(map[string]any)
	attrs := resource["attributes"].([]any)
	if attrs[0].(map[string]any)["value"].(map[string]any)["stringValue"] != "fishd-test" {
		t.Fatalf("service.name 错误：%v", attrs)
	}
	spans := resourceSpans[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)
	span := spans[0].(map[string]any)
	if len(span["traceId"].(string)) != 32 || len(span["spanId"].(string)) != 16 {
		t.Fatalf("OTLP ID 长度错误：%v", span)
	}
	if span["name"] != "llm.step" || span["kind"].(float64) != 1 {
		t.Fatalf("span 名称/类型错误：%v", span)
	}
	if span["status"].(map[string]any)["code"].(float64) != 2 {
		t.Fatalf("错误状态未导出：%v", span["status"])
	}
	if span["endTimeUnixNano"] == "" || span["startTimeUnixNano"] == "" {
		t.Fatal("时间戳缺失")
	}
}

// TestOTLPExporterRejectsBadEndpoint 非 http(s) 直接拒绝。
func TestOTLPExporterRejectsBadEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "ftp://x", "http://", "not-a-url"} {
		if _, err := NewOTLPExporter(endpoint, "", time.Second); err == nil {
			t.Fatalf("非法 endpoint 被接受：%q", endpoint)
		}
	}
}

// fakeExporter 记录导出批次。
type fakeExporter struct {
	mu    sync.Mutex
	spans []Span
}

func (f *fakeExporter) Export(_ context.Context, spans []Span) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spans = append(f.spans, spans...)
	return nil
}

// TestLocalExportsEndedSpans SetExporter 后 End 的 span 会进导出队列。
func TestLocalExportsEndedSpans(t *testing.T) {
	local, err := NewLocal(100, "")
	if err != nil {
		t.Fatal(err)
	}
	exp := &fakeExporter{}
	local.SetExporter(exp)
	ctx, span := local.StartEvent(context.Background(), "comment:1:2", "generate", A("channel", "comment"))
	_, child := local.Start(ctx, "llm.step", A("model", "m"))
	local.End(child, nil)
	local.End(span, nil)
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	local.FlushExporter(flushCtx)
	exp.mu.Lock()
	defer exp.mu.Unlock()
	if len(exp.spans) != 2 {
		t.Fatalf("导出 span 数=%d，期望 2", len(exp.spans))
	}
	for _, span := range exp.spans {
		if span.TraceID == "" || span.End.IsZero() {
			t.Fatalf("导出 span 缺字段：%+v", span)
		}
	}
}
