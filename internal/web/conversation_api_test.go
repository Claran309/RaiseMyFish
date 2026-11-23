package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// fakeConversationStats 返回固定观测，便于断言 JSON 口径。
type fakeConversationStats struct {
	stats   store.ConversationStats
	err     error
	from    time.Time
	to      time.Time
	nowSeen time.Time
}

func (f *fakeConversationStats) ConversationStats(_ context.Context, from, to, now time.Time) (store.ConversationStats, error) {
	f.from, f.to, f.nowSeen = from, to, now
	if f.err != nil {
		return store.ConversationStats{}, f.err
	}
	return f.stats, nil
}

func newConversationServer(t *testing.T, repo store.ConversationStatsRepo) *Server {
	t.Helper()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	srv, err := New(Options{
		Addr: "127.0.0.1:8090", Repo: &fakeRepo{}, Log: zap.NewNop(),
		Now: func() time.Time { return now }, Conversations: repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// TestConversationsAPIReportsMetrics 正常返回六个指标，且无基线时给 null。
func TestConversationsAPIReportsMetrics(t *testing.T) {
	delta := 12.5
	latency := 180.0
	repo := &fakeConversationStats{stats: store.ConversationStats{
		From: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC),
		Channels: []store.ConversationStat{
			{
				Channel: "dm", Active: 4, Expired: 2, WithSummary: 3, SummaryCoverage: 0.75,
				TurnsAvg: 6.5, SummaryAgeSeconds: 120, Events: 40, WithConversation: 30, HitRate: 0.75,
				ConvInputTokens: 900, PlainInputTokens: 800, TokenSamples: 20,
				PromptTokenDeltaPct: &delta, ConvP95MS: 1200, PlainP95MS: 1020, P95LatencyDeltaMS: &latency,
				OutEvents: 20, RepeatedOut: 1, RepetitionRate: 0.05,
			},
			{Channel: "comment", Active: 1, Events: 10, WithConversation: 0},
		},
		GeneratedAt: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC),
	}}
	srv := newConversationServer(t, repo)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/conversations?hours=6"))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Hours    int              `json:"hours"`
		Channels []map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Hours != 6 || len(body.Channels) != 2 {
		t.Fatalf("响应结构不符：%+v", body)
	}
	dm := body.Channels[0]
	for _, key := range []string{
		"hit_rate", "turns_avg", "summary_age_seconds", "prompt_token_delta_pct",
		"p95_latency_delta_ms", "repetition_rate",
	} {
		if _, ok := dm[key]; !ok {
			t.Fatalf("缺少指标 %s：%+v", key, dm)
		}
	}
	if dm["hit_rate"] != 0.75 || dm["prompt_token_delta_pct"] != 12.5 || dm["p95_latency_delta_ms"] != 180.0 {
		t.Fatalf("指标值不符：%+v", dm)
	}
	// 评论通道没有对照样本：增量必须是 null，不能是 0。
	comment := body.Channels[1]
	if comment["prompt_token_delta_pct"] != nil || comment["p95_latency_delta_ms"] != nil {
		t.Fatalf("无样本应给 null：%+v", comment)
	}
	if !repo.from.Equal(time.Date(2026, 11, 24, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("窗口起点错误：%v", repo.from)
	}
}

// TestConversationsAPIRejectsBadHours 参数越界直接 400。
func TestConversationsAPIRejectsBadHours(t *testing.T) {
	srv := newConversationServer(t, &fakeConversationStats{})
	for _, target := range []string{"/api/conversations?hours=0", "/api/conversations?hours=999", "/api/conversations?hours=abc"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, readReq(target))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 状态码=%d", target, rec.Code)
		}
	}
}

// TestConversationsAPIDisabled 未装配时返回 503。
func TestConversationsAPIDisabled(t *testing.T) {
	srv := newConversationServer(t, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/conversations"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码=%d", rec.Code)
	}
}
