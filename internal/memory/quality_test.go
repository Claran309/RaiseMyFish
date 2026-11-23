package memory

import "testing"

func TestClassifyValueStableVsNoise(t *testing.T) {
	keep := []string{"我喜欢吃白饭", "主人以后别熬夜", "我习惯每天十点睡", "我一直不喜欢香菜"}
	for _, text := range keep {
		if decision := ClassifyValue(text); !decision.Keep || decision.Reason != "stable_signal" {
			t.Errorf("%q 应保留：%+v", text, decision)
		}
	}
	drop := map[string]string{
		"哈哈哈哈":      "joke_or_repeat",
		"主人！":       "too_short",
		"主人今天来了":    "no_stable_signal",
		"？？？":       "symbol_only",
		"笑死，主人也这样":  "joke_or_repeat",
		"喜欢！！！！！！！": "noise_punctuation",
	}
	for text, want := range drop {
		if decision := ClassifyValue(text); decision.Keep || decision.Reason != want {
			t.Errorf("%q 应丢弃（%s）：%+v", text, want, decision)
		}
	}
}

// TestExtractKeepsOwnerCandidateForWriteGate 候选层保持旧语义；
// 写入质量由 ClassifyValue 决定，避免破坏已验收的候选契约。
func TestExtractKeepsOwnerCandidateForWriteGate(t *testing.T) {
	opts := ExtractOptions{OwnerUID: "owner-1"}
	ev := Event{ID: "live:room:1", Channel: "live", Dir: "in", Session: "room", FromID: "2002", FromName: "乙", Text: "主人今天也在"}
	found := false
	for _, c := range Extract(ev, opts) {
		if c.Kind == KindPreference {
			found = true
		}
	}
	if !found {
		t.Fatal("候选层应保持主人相关偏好")
	}
	if ClassifyValue("主人今天也在").Keep {
		t.Fatal("无稳定信号不应通过写入闸门")
	}
}
