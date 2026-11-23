package trace

// 本文件实现可选的 OTLP/HTTP JSON 导出（B2）。
//
// 刻意不引入 OpenTelemetry SDK：OTLP 接收端（Jaeger/Collector）接受
// `POST /v1/traces` 的 JSON 载荷，用标准库就能导出；这样默认构建仍是零新增依赖，
// 只有显式配置 endpoint 时才启用。
//
// 隐私：只导出 span 元数据（ID/名称/耗时/白名单属性），不导出 prompt/正文/凭据。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Exporter 批量导出已结束的 span；实现必须自己限时且可失败降级。
type Exporter interface {
	Export(ctx context.Context, spans []Span) error
}

// OTLPExporter 是 OTLP/HTTP JSON 导出器。
type OTLPExporter struct {
	endpoint string
	service  string
	client   *http.Client
}

// NewOTLPExporter 校验 endpoint（http/https）并构造导出器。
func NewOTLPExporter(endpoint, service string, timeout time.Duration) (*OTLPExporter, error) {
	endpoint = strings.TrimSpace(endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("OTLP endpoint 必须是 http/https URL")
	}
	service = strings.TrimSpace(service)
	if service == "" {
		service = "fishd"
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if timeout > 30*time.Second {
		return nil, errors.New("OTLP 超时不能超过30秒")
	}
	return &OTLPExporter{endpoint: endpoint, service: service, client: &http.Client{Timeout: timeout}}, nil
}

// otlpPayload 只覆盖我们用到的字段，避免依赖 SDK 的完整模型。
type otlpPayload struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource    `json:"resource"`
	ScopeSpans []otlpScopeSpan `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes"`
}

type otlpScopeSpan struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpSpan struct {
	TraceID           string          `json:"traceId"`
	SpanID            string          `json:"spanId"`
	ParentSpanID      string          `json:"parentSpanId,omitempty"`
	Name              string          `json:"name"`
	Kind              int             `json:"kind"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	EndTimeUnixNano   string          `json:"endTimeUnixNano"`
	Attributes        []otlpAttribute `json:"attributes,omitempty"`
	Status            *otlpStatus     `json:"status,omitempty"`
}

type otlpAttribute struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

// Export 发送一批 span；非 2xx 视为失败。
func (e *OTLPExporter) Export(ctx context.Context, spans []Span) error {
	if e == nil || len(spans) == 0 {
		return nil
	}
	converted := make([]otlpSpan, 0, len(spans))
	for _, span := range spans {
		converted = append(converted, e.convert(span))
	}
	payload := otlpPayload{ResourceSpans: []otlpResourceSpans{{
		Resource: otlpResource{Attributes: []otlpAttribute{
			attrString("service.name", e.service),
		}},
		ScopeSpans: []otlpScopeSpan{{Scope: otlpScope{Name: "fishd.trace"}, Spans: converted}},
	}}}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化 OTLP：%w", err)
	}
	endpoint := e.endpoint
	if !strings.HasSuffix(endpoint, "/v1/traces") {
		endpoint = strings.TrimRight(endpoint, "/") + "/v1/traces"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造 OTLP 请求：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("发送 OTLP：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("OTLP 接收端返回 %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (e *OTLPExporter) convert(span Span) otlpSpan {
	out := otlpSpan{
		TraceID:           otlpTraceID(span.TraceID),
		SpanID:            otlpSpanID(span.SpanID),
		ParentSpanID:      otlpSpanID(span.ParentID),
		Name:              span.Name,
		Kind:              1,
		StartTimeUnixNano: fmt.Sprintf("%d", span.Start.UnixNano()),
		EndTimeUnixNano:   fmt.Sprintf("%d", span.End.UnixNano()),
	}
	for key, value := range span.Attributes {
		if attr, ok := otlpAttr(key, value); ok {
			out.Attributes = append(out.Attributes, attr)
		}
	}
	// map 顺序不稳定；按 key 排序保证同一 span 多次导出字节稳定。
	sortAttrs(out.Attributes)
	if span.Error != "" {
		out.Status = &otlpStatus{Code: 2, Message: span.Error}
	}
	return out
}

// otlpTraceID 把 16 位 hex 的事件 trace 派生为 32 位 hex（OTLP 要求 16 字节）。
func otlpTraceID(traceID string) string {
	sum := sha256.Sum256([]byte(traceID))
	return hex.EncodeToString(sum[:16])
}

// otlpSpanID 保证 16 位 hex；非法/空值返回空串（OTLP 允许缺 parent）。
func otlpSpanID(spanID string) string {
	if len(spanID) == 16 {
		return spanID
	}
	if spanID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(spanID))
	return hex.EncodeToString(sum[:8])
}

func otlpAttr(key string, value any) (otlpAttribute, bool) {
	if key == "" {
		return otlpAttribute{}, false
	}
	switch v := value.(type) {
	case string:
		return attrString(key, v), true
	case bool:
		return otlpAttribute{Key: key, Value: otlpAnyValue{BoolValue: &v}}, true
	case int:
		s := fmt.Sprintf("%d", v)
		return otlpAttribute{Key: key, Value: otlpAnyValue{IntValue: &s}}, true
	case int64:
		s := fmt.Sprintf("%d", v)
		return otlpAttribute{Key: key, Value: otlpAnyValue{IntValue: &s}}, true
	case float64:
		return otlpAttribute{Key: key, Value: otlpAnyValue{DoubleValue: &v}}, true
	default:
		// 未知类型不导出：宁缺毋滥，避免把正文类对象带出去。
		return otlpAttribute{}, false
	}
}

func attrString(key, value string) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpAnyValue{StringValue: &value}}
}

func sortAttrs(attrs []otlpAttribute) {
	for i := 1; i < len(attrs); i++ {
		for j := i; j > 0 && attrs[j].Key < attrs[j-1].Key; j-- {
			attrs[j], attrs[j-1] = attrs[j-1], attrs[j]
		}
	}
}
