package memoryeval

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func runFile(t *testing.T, path string) Report {
	t.Helper()
	cases, err := LoadCases(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// TestMemoryCasesPass 仓库里的 100 条用例必须全过（规则版能力边界内）。
func TestMemoryCasesPass(t *testing.T) {
	path := filepath.Join("..", "..", "personas", "dafeiyu", "eval", "memory-cases.jsonl")
	report := runFile(t, path)
	if report.Cases != 100 {
		t.Fatalf("用例数=%d，期望 100", report.Cases)
	}
	if !report.Passed {
		for _, item := range report.Detail {
			if !item.Passed {
				t.Errorf("失败用例 %s：%s", item.ID, item.Reason)
			}
		}
		t.Fatalf("指标未达标：%+v", report)
	}
	if report.Recall != 1 || report.FalsePositive != 0 || report.PrivacyLeak != 0 || report.KeyAccuracy != 1 {
		t.Fatalf("口径不符：%+v", report)
	}
}

// TestRunDetectsMissedRemember 该记住却没提取到必须判失败。
func TestRunDetectsMissedRemember(t *testing.T) {
	report, err := Run(context.Background(), []Case{
		{ID: "a", Type: "remember", Text: "今天天气不错", ExpectKind: "promise"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.RememberHit != 0 {
		t.Fatalf("漏报未识别：%+v", report)
	}
}

// TestRunDetectsPrivacyLeak 未授权私密事件产出候选必须算泄漏。
func TestRunDetectsPrivacyLeak(t *testing.T) {
	report, err := Run(context.Background(), []Case{
		// 私密事件却误开了 AllowPrivate：评测必须把它算成泄漏。
		{ID: "p", Type: "privacy", Text: "我答应明天来", Private: true, AllowPravt: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.PrivacyLeak != 1 || report.Passed {
		t.Fatalf("泄漏未识别：%+v", report)
	}
}

// TestLoadRejectsUnknownType 未知类型直接报错，不静默跳过。
func TestLoadRejectsUnknownType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"x","type":"made_up","text":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCases(path); err == nil {
		t.Fatal("未知类型被接受")
	}
}
