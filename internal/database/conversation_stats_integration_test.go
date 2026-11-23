//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/llmmeta"
	"RaiseMyFish/internal/store"
)

// TestConversationStatsRealPostgres 验证 C8 指标从真实表算出来：
// 命中率、轮数、摘要覆盖率、token/延迟对照与重复回复率。
func TestConversationStatsRealPostgres(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	conversations, err := NewConversationRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := NewStats(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := now.Add(-10 * time.Minute)
	conv := "dm:9001:9002"
	for i, item := range []struct {
		id, text, conv string
	}{
		{"dm:9001:9002:1", "你记得我吗", conv},
		{"dm:9001:9002:2", "那上次说的鱼呢", conv},
		{"dm:9001:9002:3", "没有会话键的老消息", ""},
	} {
		insertEvent(t, events, event.Event{
			ID: item.id, Channel: event.ChDM, Dir: event.DirIn, Session: "9001:9002",
			FromID: "9002", Text: item.text, Private: true, At: base.Add(time.Duration(i) * time.Second),
			Status: event.StatusPending, CollectedAt: base, CollectMode: "poll", ConversationID: item.conv,
		})
	}
	for i, text := range []string{"记得，你是养鱼那位", "记得，你是养鱼那位"} {
		insertEvent(t, events, event.Event{
			ID: "dm:out:9001:9002:" + string(rune('1'+i)), Channel: event.ChDM, Dir: event.DirOut,
			Session: "9001:9002", FromID: "9001", Text: text, Private: true,
			ReplyTo: "dm:9001:9002:1", At: base.Add(time.Duration(10+i) * time.Second),
			Status: event.StatusDone, ConversationID: conv,
		})
	}
	sentAt := []time.Time{base.Add(12 * time.Second), base.Add(13 * time.Second), base.Add(14 * time.Second)}
	for i, id := range []string{"dm:9001:9002:1", "dm:9001:9002:2", "dm:9001:9002:3"} {
		if err := db.Exec(`INSERT INTO send_log(event_id,channel,ok,verified,msg_id,at) VALUES(?,?,?,?,?,?)`,
			id, "dm", true, true, "m"+id, sentAt[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, item := range []struct {
		id     string
		tokens int
	}{
		{"dm:9001:9002:1", 1000},
		{"dm:9001:9002:3", 800},
	} {
		tokens := item.tokens
		if err := usage.RecordUsage(ctx, store.Usage{
			At: base.Add(time.Duration(i) * time.Second), Channel: event.ChDM,
			Purpose: string(llmmeta.PurposeReply), CorrelationID: item.id,
			Model: "mock:model", InputTokens: &tokens, Milliseconds: 10, OK: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := conversations.Touch(ctx, store.ConversationState{
		ConversationID: conv, Channel: "dm", AccountID: "9001", UserID: "9002",
		Summary: "在聊鱼", TurnCount: 6, LastEventID: "dm:9001:9002:2",
		LastTurnAt: base, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE conversation_state SET summary_updated_at = ? WHERE conversation_id = ?`,
		now.Add(-5*time.Minute), conv).Error; err != nil {
		t.Fatal(err)
	}
	if err := conversations.Touch(ctx, store.ConversationState{
		ConversationID: "dm:1:2", Channel: "dm", TurnCount: 2,
		LastTurnAt: base, ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := stats.ConversationStats(ctx, now.Add(-time.Hour), now, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Channels) != 2 {
		t.Fatalf("通道数=%d", len(got.Channels))
	}
	dm := got.Channels[0]
	if dm.Channel != "dm" {
		t.Fatalf("通道顺序错误：%+v", got.Channels)
	}
	if dm.Active != 1 || dm.Expired != 1 || dm.WithSummary != 1 || dm.SummaryCoverage != 1 {
		t.Fatalf("会话状态口径错误：%+v", dm)
	}
	if dm.TurnsAvg != 6 {
		t.Fatalf("平均轮数=%v，期望 6", dm.TurnsAvg)
	}
	if dm.Events != 3 || dm.WithConversation != 2 {
		t.Fatalf("命中率样本错误：%+v", dm)
	}
	if dm.HitRate < 0.66 || dm.HitRate > 0.67 {
		t.Fatalf("命中率=%v", dm.HitRate)
	}
	if dm.TokenSamples != 2 || dm.PromptTokenDeltaPct == nil {
		t.Fatalf("token 对照缺失：%+v", dm)
	}
	if delta := *dm.PromptTokenDeltaPct; delta != 25 {
		t.Fatalf("token 增量=%v，期望 25", delta)
	}
	if dm.P95LatencyDeltaMS == nil {
		t.Fatalf("延迟增量应可算：%+v", dm)
	}
	if dm.OutEvents != 2 || dm.RepeatedOut != 1 || dm.RepetitionRate != 0.5 {
		t.Fatalf("重复回复口径错误：%+v", dm)
	}
	comment := got.Channels[1]
	if comment.PromptTokenDeltaPct != nil || comment.P95LatencyDeltaMS != nil {
		t.Fatalf("无对照样本却给了增量：%+v", comment)
	}
}
