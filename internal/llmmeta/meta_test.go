package llmmeta

// 本文件测 F07 的调用元数据：用途合并、安全关联 ID 与通道归属规则。

import (
	"context"
	"strings"
	"testing"
)

func TestWithCallMergesAndFromReads(t *testing.T) {
	ctx := context.Background()
	if got := From(ctx); got != (Call{}) {
		t.Fatalf("未标注时应有零值：%+v", got)
	}
	if got := From(nil); got != (Call{}) {
		t.Fatalf("nil ctx 应返回零值：%+v", got)
	}

	ctx = WithCall(ctx, Call{Purpose: PurposeEmbedIndex, CorrelationID: "knowledge"})
	if got := From(ctx); got.Purpose != PurposeEmbedIndex || got.CorrelationID != "knowledge" {
		t.Fatalf("字段未写入：%+v", got)
	}

	// 空字段不覆盖已有值：调用方只需补自己知道的部分。
	ctx = WithCall(ctx, Call{CorrelationID: "memory"})
	if got := From(ctx); got.Purpose != PurposeEmbedIndex || got.CorrelationID != "memory" {
		t.Fatalf("空用途覆盖了已有值：%+v", got)
	}

	// nil ctx 也要能拿到可用 context，而不是 panic。
	fromNil := WithCall(nil, Call{Purpose: PurposeReply})
	if got := From(fromNil); got.Purpose != PurposeReply {
		t.Fatalf("nil ctx 合并失败：%+v", got)
	}
}

func TestSafeCorrelationID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"事件ID", "live:30308220:123456", "live:30308220:123456"},
		{"带空白", "  live:room:1  ", "live:room:1"},
		{"空串", "", ""},
		{"纯空白", "   ", ""},
		{"中文昵称", "小明", ""},
		{"正文换行", "live:room:1\n伪造行", ""},
		{"控制字符", "live:\x01", ""},
		{"超长", strings.Repeat("a", 129), ""},
		{"边界长度", strings.Repeat("a", 128), strings.Repeat("a", 128)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SafeCorrelationID(tc.in); got != tc.want {
				t.Fatalf("SafeCorrelationID(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPurposeSets(t *testing.T) {
	for _, p := range []Purpose{
		PurposeReply, PurposeReact, PurposeVision,
		PurposeMemoryExtract, PurposeMemeLearn,
		PurposeEmbedIndex, PurposeEmbedQuery, PurposeUnknown,
	} {
		if !KnownPurpose(string(p)) {
			t.Errorf("已知用途被拒绝：%s", p)
		}
	}
	for _, p := range []string{"", "made_up", "REPLY", "reply "} {
		if KnownPurpose(p) {
			t.Errorf("非法用途被接受：%q", p)
		}
	}

	// 对话通道内的调用必须带通道；后台增强与 embedding 不属于任何通道。
	for _, p := range []Purpose{PurposeReply, PurposeReact, PurposeVision} {
		if !ChannelBound(p) {
			t.Errorf("%s 应绑定通道", p)
		}
	}
	for _, p := range []Purpose{PurposeMemoryExtract, PurposeMemeLearn, PurposeEmbedIndex, PurposeEmbedQuery, PurposeUnknown} {
		if ChannelBound(p) {
			t.Errorf("%s 不应绑定通道", p)
		}
	}
}
