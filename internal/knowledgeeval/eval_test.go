package knowledgeeval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
)

type testReader struct {
	items []knowledge.Item
	err   error
}

func TestFrozenCaseShapeAndStrictLoading(t *testing.T) {
	data, err := os.ReadFile("testdata/cases.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := ReadCases(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 100 {
		t.Fatal(len(cases))
	}
	for _, bad := range []string{strings.Replace(string(data), `"kind":"positive"`, `"kind":"unknown"`, 1), strings.Replace(string(data), `"id":"pos-002"`, `"id":"pos-001"`, 1), strings.Replace(string(data), `"query":`, `"unexpected":1,"query":`, 1)} {
		if _, err := ReadCases(strings.NewReader(bad)); err == nil {
			t.Fatal("未拒绝坏冻结集")
		}
	}
}

func TestOutputDirectoryNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("不可覆盖"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunOffline(context.Background(), "missing", "missing", dir); err == nil {
		t.Fatal("允许复用输出目录")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil || string(got) != "不可覆盖" {
		t.Fatal("旧证据被改写")
	}
}

func (r testReader) Recall(ctx context.Context, q knowledge.Query) (knowledge.Context, error) {
	if err := ctx.Err(); err != nil {
		return knowledge.Context{}, err
	}
	return knowledge.Context{Scope: q.Scope, Revision: strings.Repeat("a", 64), Items: r.items}, r.err
}
func testScope() knowledge.Scope { return knowledge.Scope{Channel: event.ChLive, Session: "test-room"} }
func testItem(id string) knowledge.Item {
	return knowledge.Item{ID: id, Source: "test:" + id, Text: "测试资料", Scenes: []string{"live"}, Priority: 50}
}
func testCase(id, kind string, expected ...string) QueryCase {
	return QueryCase{ID: id, Kind: kind, Form: "natural", Query: "测试问题", Scope: testScope(), ExpectedIDs: expected, Labels: []string{"测试"}, Rationale: "开发测试"}
}

func TestActualReturnPrecisionAndCoverage(t *testing.T) {
	cases := []QueryCase{testCase("p", "positive", "a"), testCase("n", "negative")}
	r, err := (Evaluator{Public: testReader{items: []knowledge.Item{testItem("a"), testItem("b")}}}).Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.Returned != 4 || r.Metrics.Relevant != 1 || r.Metrics.Precision == nil || *r.Metrics.Precision != 0.25 || r.Metrics.PositiveHits != 1 || r.Metrics.NegativeFalse != 1 {
		t.Fatalf("实际条目分母/覆盖错误：%+v", r.Metrics)
	}
}
func TestZeroReturnsAreNotPerfectPrecision(t *testing.T) {
	r, err := (Evaluator{Public: testReader{}}).Run(context.Background(), []QueryCase{testCase("p", "positive", "a")})
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.Precision != nil || r.Metrics.PositiveHits != 0 || r.Passed {
		t.Fatal("零返回被误报为满分")
	}
}
func TestInvalidOrOverThreeContextFails(t *testing.T) {
	for _, items := range [][]knowledge.Item{{testItem("a"), testItem("a")}, {testItem("a"), testItem("b"), testItem("c"), testItem("d")}} {
		if _, err := (Evaluator{Public: testReader{items: items}}).Run(context.Background(), []QueryCase{testCase("p", "positive", "a")}); err == nil {
			t.Fatal("非法/超过Top3结果未拒绝")
		}
	}
}
func TestReaderErrorAndCancelNotEmptyRecall(t *testing.T) {
	want := errors.New("读取失败")
	if _, err := (Evaluator{Public: testReader{err: want}}).Run(context.Background(), []QueryCase{testCase("n", "negative")}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Evaluator{Public: testReader{}}).Run(ctx, []QueryCase{testCase("n", "negative")}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestBudgetUsesActualBrainWithOfflineSinks(t *testing.T) {
	k := knowledge.Context{Scope: testScope(), Revision: strings.Repeat("a", 64), Items: []knowledge.Item{testItem("a")}}
	audit, err := CheckBudget(context.Background(), knowledge.Query{Scope: testScope(), Text: "测试问题"}, k)
	if err != nil {
		t.Fatal(err)
	}
	if !audit.Passed || audit.Bytes <= 0 || audit.Bytes > 3276 || audit.PromptEstimate > 32768 || audit.ProviderCalls != 2 || audit.UsageRows != 2 {
		t.Fatalf("预算检查失败：%+v", audit)
	}
	k.Scope.Session = "other"
	if _, err := CheckBudget(context.Background(), knowledge.Query{Scope: testScope(), Text: "测试问题"}, k); err == nil {
		t.Fatal("错配范围仍通过")
	}
}
