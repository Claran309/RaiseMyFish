package personaeval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeGenerator struct{ calls int }

func (g *fakeGenerator) Name() string { return "test:model" }
func (g *fakeGenerator) Generate(_ context.Context, in Input) (Generation, error) {
	g.calls++
	switch in.ID {
	case "good":
		return Generation{RawOutput: "本鱼在", DeliveredOutput: "本鱼在"}, nil
	case "long":
		return Generation{RawOutput: strings.Repeat("鱼", 41), DeliveredOutput: "鱼"}, nil
	default:
		return Generation{}, errors.New("synthetic failure")
	}
}

func TestRunPreservesDenominatorAndRawDeliveredDifference(t *testing.T) {
	s, err := NewScorer()
	if err != nil {
		t.Fatal(err)
	}
	f, err := Prepare(context.Background(), "../../personas/dafeiyu", "../../personas/dafeiyu/eval/cases.jsonl", s)
	if err != nil {
		t.Fatal(err)
	}
	f.Cases = []Case{{ID: "good", Channel: "live", Kind: "normal", Input: "你好", Expect: []string{"认身份"}, Forbid: []string{"长句"}}, {ID: "long", Channel: "live", Kind: "normal", Input: "你好", Expect: []string{"认身份"}, Forbid: []string{"长句"}}, {ID: "error", Channel: "live", Kind: "normal", Input: "你好", Expect: []string{"认身份"}, Forbid: []string{"长句"}}}
	dir := filepath.Join(t.TempDir(), "run")
	report, err := Run(context.Background(), f, s, &fakeGenerator{}, RunOptions{RunID: "test", OutputDir: dir, SourceKind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 3 || report.Completed != 3 || report.RawPassed != 1 || report.DeliveredPassed != 2 || report.Reached90 {
		t.Fatalf("统计被美化：%+v", report)
	}
	if _, err := os.Stat(filepath.Join(dir, "results.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "report.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), f, s, &fakeGenerator{}, RunOptions{RunID: "test", OutputDir: dir, SourceKind: "test"}); err == nil {
		t.Fatal("覆盖了旧证据目录")
	}
}

func TestRedactionDoesNotPublishCredential(t *testing.T) {
	r := NewRedactor([]string{"exact-secret-value"})
	if !r.Contains("echo exact-secret-value") {
		t.Fatal("未发现真实凭据")
	}
	if got := r.Text("echo exact-secret-value"); strings.Contains(got, "exact-secret-value") {
		t.Fatal("未脱敏")
	}
}

type fatalLastGenerator struct{ calls int }

func (g *fatalLastGenerator) Name() string { return "unit:model" }
func (g *fatalLastGenerator) Generate(context.Context, Input) (Generation, error) {
	g.calls++
	if g.calls == 35 {
		return Generation{RawOutput: "本鱼", DeliveredOutput: "本鱼", Fatal: true}, errors.New("synthetic ledger failure")
	}
	return Generation{RawOutput: "本鱼", DeliveredOutput: "本鱼"}, nil
}

func TestFatalLastCaseCannotProduceAcceptedRun(t *testing.T) {
	s, err := NewScorer()
	if err != nil {
		t.Fatal(err)
	}
	f, err := Prepare(context.Background(), "../../personas/dafeiyu", "../../personas/dafeiyu/eval/cases.jsonl", s)
	if err != nil {
		t.Fatal(err)
	}
	f.Cases = nil
	for i := range 35 {
		f.Cases = append(f.Cases, Case{ID: fmt.Sprintf("case-%d", i), Channel: "live", Kind: "normal", Input: "你好", Expect: []string{"认身份"}, Forbid: []string{"长句"}})
	}
	report, err := Run(context.Background(), f, s, &fatalLastGenerator{}, RunOptions{RunID: "fatal-test", OutputDir: filepath.Join(t.TempDir(), "run"), SourceKind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if report.RawPassed != 34 || report.Total != 35 {
		t.Fatal("不应隐藏已得到的分数")
	}
	if report.Complete || report.Reached90 {
		t.Fatal("最后一条致命失败被当作整轮验收通过")
	}
}

type cancelLastGenerator struct {
	calls  int
	cancel context.CancelFunc
}

func (g *cancelLastGenerator) Name() string { return "unit:model" }
func (g *cancelLastGenerator) Generate(context.Context, Input) (Generation, error) {
	g.calls++
	if g.calls == 35 {
		g.cancel()
		return Generation{}, context.Canceled
	}
	return Generation{RawOutput: "本鱼", DeliveredOutput: "本鱼"}, nil
}

func TestCancellationInLastCaseIsIncomplete(t *testing.T) {
	s, err := NewScorer()
	if err != nil {
		t.Fatal(err)
	}
	f, err := Prepare(context.Background(), "../../personas/dafeiyu", "../../personas/dafeiyu/eval/cases.jsonl", s)
	if err != nil {
		t.Fatal(err)
	}
	f.Cases = nil
	for i := range 35 {
		f.Cases = append(f.Cases, Case{ID: fmt.Sprintf("case-%d", i), Channel: "live", Kind: "normal", Input: "你好", Expect: []string{"认身份"}, Forbid: []string{"长句"}})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	report, err := Run(ctx, f, s, &cancelLastGenerator{cancel: cancel}, RunOptions{RunID: "cancel-test", OutputDir: filepath.Join(t.TempDir(), "run"), SourceKind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Completed != 35 || report.Complete || report.Reached90 {
		t.Fatal("末条取消被当作完整运行")
	}
}
