package reasoning

import (
	"strings"
	"testing"
)

func TestReflectStatAddAndRates(t *testing.T) {
	var stat ReflectStat
	stat.Add(Result{ReflectCalled: true, ReflectChanged: true, ReflectPromptBytes: 100})
	stat.Add(Result{ReflectCalled: true, ReflectChanged: false, ReflectPromptBytes: 120})
	stat.Add(Result{ReflectSkippedReason: "timeout"})
	stat.Add(Result{ReflectSkippedReason: "timeout"})
	stat.Add(Result{ReflectSkippedReason: "disabled"})

	if stat.Runs != 5 || stat.Called != 2 || stat.Changed != 1 || stat.Skipped != 3 {
		t.Fatalf("计数错误：%+v", stat)
	}
	if stat.ChangedRate() != 0.5 || stat.SkipRate() != 0.6 {
		t.Fatalf("比率错误：%+v", stat)
	}
	if stat.SkipReasons["timeout"] != 2 || stat.SkipReasons["disabled"] != 1 {
		t.Fatalf("跳过原因未聚合：%+v", stat.SkipReasons)
	}
	if got := stat.String(); !strings.Contains(got, "reasons=[disabled=1,timeout=2]") {
		t.Fatalf("摘要不稳定：%s", got)
	}
}

func TestReflectComparisonHarmAlarm(t *testing.T) {
	comparison := ReflectComparison{On: ReflectStat{Called: 10, Changed: 10}, Improved: 8, Harmed: 1}
	if comparison.ImproveRate() != 0.8 || comparison.HarmRate() != 0.1 || !comparison.HarmAlarm() {
		t.Fatalf("对照指标错误：%+v", comparison)
	}
	comparison.Harmed = 0
	if comparison.HarmAlarm() {
		t.Fatal("无伤害仍告警")
	}
}
