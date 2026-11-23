package extractworker

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

// TestPreferenceGateBlocksLowValueOwnerMention 开启闸门后：
// 只提「主人」的偏好不写正式表；带稳定信号的偏好照写。
func TestPreferenceGateBlocksLowValueOwnerMention(t *testing.T) {
	noise := mkEvent("live:1:9001", baseTime, "主人今天也在", "路人", false, event.ChLive, "1")
	stable := mkEvent("live:1:9002", baseTime.Add(time.Minute), "主人以后别熬夜了", "路人", false, event.ChLive, "1")
	src := &fakeSource{events: []event.Event{noise, stable}}
	repo := &fakeRepo{}
	stats, err := newWorker(t, src, repo, NewMemoryWatermark(), Options{PreferenceGate: true}).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	recs := repo.all()
	if len(recs) != 1 || recs[0].Summary != "主人以后别熬夜了" {
		t.Fatalf("闸门结果不符：stats=%+v recs=%+v", stats, recs)
	}
}

// TestPreferenceGateOffKeepsLegacyBehavior 关闭闸门时保持旧契约（主人相关候选照写）。
func TestPreferenceGateOffKeepsLegacyBehavior(t *testing.T) {
	noise := mkEvent("live:1:9003", baseTime, "主人今天也在", "路人", false, event.ChLive, "1")
	src := &fakeSource{events: []event.Event{noise}}
	repo := &fakeRepo{}
	if _, err := newWorker(t, src, repo, NewMemoryWatermark(), Options{PreferenceGate: false}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.all()) != 1 {
		t.Fatalf("关闭闸门后旧行为被改变：%+v", repo.all())
	}
}
