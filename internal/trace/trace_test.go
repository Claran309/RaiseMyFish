package trace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartEventBuildsStableStageChain(t *testing.T) {
	local, err := NewLocal(1000, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, root := local.StartEvent(context.Background(), "comment:1:2", "collect", A("channel", "comment"))
	if ctx == nil || root == nil || root.TraceID != TraceID("comment:1:2") {
		t.Fatalf("根 span 错误：%+v", root)
	}
	local.End(root, nil)
	_, schedule := local.StartEvent(context.Background(), "comment:1:2", "schedule")
	if schedule.ParentID != root.SpanID {
		t.Fatalf("阶段链父ID=%q，期望 %q", schedule.ParentID, root.SpanID)
	}
	local.End(schedule, nil)
	_, child := local.Start(ctx, "context.build", A("memory_ids", 2))
	local.End(child, nil)
	detail := local.Get(root.TraceID)
	if detail.SpanCount != 3 || detail.EventID != "comment:1:2" {
		t.Fatalf("trace 聚合错误：%+v", detail)
	}
	if detail.Spans[2].ParentID != root.SpanID || detail.Spans[2].Attributes["memory_ids"] != 2 {
		t.Fatalf("子 span 未挂到根：%+v", detail.Spans[2])
	}
}

func TestRecentSortsSlowAndJSONLOnlyMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traces.jsonl")
	local, err := NewLocal(1000, path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, slow := local.StartEvent(context.Background(), "live:1:1", "generate", A("channel", "live"))
	time.Sleep(3 * time.Millisecond)
	_, child := local.Start(ctx, "llm.step", A("model", "m"), A("input_tokens", 10))
	local.End(child, nil)
	local.End(slow, nil)
	_, fast := local.StartEvent(context.Background(), "live:1:2", "generate")
	local.End(fast, nil)
	recent := local.Recent(10, false)
	if len(recent) != 2 {
		t.Fatalf("trace 数量=%d", len(recent))
	}
	slowList := local.Recent(10, true)
	if slowList[0].SpanCount != 2 || slowList[0].DurationMS < 1 {
		t.Fatalf("慢 trace 排序错误：%+v", slowList)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"llm.step"`) || !strings.Contains(text, `"input_tokens":10`) {
		t.Fatalf("JSONL 缺少 span 元数据：%s", text)
	}
	if strings.Contains(text, "不存在") || strings.Contains(text, "prompt") {
		t.Fatalf("JSONL 不应写正文/敏感字段：%s", text)
	}
}
