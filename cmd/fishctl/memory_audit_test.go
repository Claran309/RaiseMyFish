package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

type fakeAuditRepo struct {
	report store.MemoryAuditReport
	err    error
	limit  int
}

func (f *fakeAuditRepo) AuditMemories(_ context.Context, limit int) (store.MemoryAuditReport, error) {
	f.limit = limit
	return f.report, f.err
}

// TestExecuteMemoryAuditOutputsAllSections 四类清单都要出现，空清单是 [] 不是 null。
func TestExecuteMemoryAuditOutputsAllSections(t *testing.T) {
	repo := &fakeAuditRepo{report: store.MemoryAuditReport{
		Since:  time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		Active: 42,
		Duplicates: []store.MemoryAuditItem{
			{ID: "fact:1", Title: "喜欢钓鲫鱼", Kind: "fact", Count: 2},
		},
		Conflicts: []store.MemoryAuditItem{
			{ID: "fact:2", Title: "改口", Count: 1},
		},
	}}
	var out bytes.Buffer
	if err := executeMemoryAudit(context.Background(), repo, 10, &out); err != nil {
		t.Fatal(err)
	}
	if repo.limit != 10 {
		t.Fatalf("limit=%d", repo.limit)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"active", "usage_since", "never_referenced", "duplicates", "conflicts", "missing_from_id"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("输出缺字段 %s：%s", key, out.String())
		}
	}
	for _, key := range []string{"never_referenced", "duplicates", "conflicts", "missing_from_id"} {
		if _, ok := body[key].([]any); !ok {
			t.Fatalf("%s 应为数组：%v", key, body[key])
		}
	}
	if body["active"].(float64) != 42 {
		t.Fatalf("active=%v", body["active"])
	}
}

// TestExecuteMemoryAuditWithoutUsageData 没有归因数据时明确说明不可判定。
func TestExecuteMemoryAuditWithoutUsageData(t *testing.T) {
	var out bytes.Buffer
	if err := executeMemoryAudit(context.Background(), &fakeAuditRepo{}, 5, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "不可判定") {
		t.Fatalf("缺少归因说明：%s", out.String())
	}
	if strings.Contains(out.String(), `"usage_since"`) {
		t.Fatalf("没有数据却给了 usage_since：%s", out.String())
	}
}

// TestExecuteMemoryAuditPropagatesError 查询失败不能吞成空报告。
func TestExecuteMemoryAuditPropagatesError(t *testing.T) {
	err := executeMemoryAudit(context.Background(), &fakeAuditRepo{err: errors.New("库挂了")}, 5, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "读取记忆审计") {
		t.Fatalf("错误被吞：%v", err)
	}
}

// TestParseMemoryAuditArgs audit 子命令的默认值与边界。
func TestParseMemoryAuditArgs(t *testing.T) {
	spec, err := parseMemoryArgs([]string{"audit"}, &bytes.Buffer{})
	if err != nil || spec.Action != "audit" || spec.Limit != 20 {
		t.Fatalf("默认解析错误：%+v %v", spec, err)
	}
	spec, err = parseMemoryArgs([]string{"audit", "--limit", "5"}, &bytes.Buffer{})
	if err != nil || spec.Limit != 5 {
		t.Fatalf("limit 解析错误：%+v %v", spec, err)
	}
	if _, err := parseMemoryArgs([]string{"audit", "--limit", "0"}, &bytes.Buffer{}); err == nil {
		t.Fatal("limit=0 被接受")
	}
}
