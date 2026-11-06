//go:build integration

package database

// M11：Records 的选取同时受相关性、主体配额和重要度约束，真实 PG 验证。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// rankingRecord 造一条公开直播摘要；fromID 为空时按主题分组。
func rankingRecord(id, title, summary, fromID string, importance float64, at time.Time) store.MemoryRecord {
	return store.MemoryRecord{
		Record: memory.Record{
			ID: id, Channel: event.ChLive, Session: "room", Summary: summary,
			Importance: importance, Confidence: 0.9, ValidFrom: at,
		},
		Source: "owner_direct", Kind: "note", Title: title, FromID: fromID,
	}
}

// rankingIDs 取出 ID 列表，断言顺序与内容。
func rankingIDs(records []memory.Record) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

func rankingWant(t *testing.T, got []memory.Record, want ...string) {
	t.Helper()
	ids := rankingIDs(got)
	if len(ids) != len(want) {
		t.Fatalf("记录 = %v，期望 %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("记录 = %v，期望 %v", ids, want)
		}
	}
}

// TestMemoryRecordsPreferRelevant 高重要度但无关的摘要不能挤掉相关摘要。
func TestMemoryRecordsPreferRelevant(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	seeds := []store.MemoryRecord{
		rankingRecord("heavy-a", "旧话题一", "固定主题一", "uid-heavy", 0.99, base),
		rankingRecord("heavy-b", "旧话题二", "固定主题二", "uid-heavy", 0.98, base),
		rankingRecord("heavy-c", "旧话题三", "固定主题三", "uid-heavy", 0.97, base),
		rankingRecord("rel", "白饭", "白饭很好吃", "uid-other", 0.2, base),
	}
	for _, rec := range seeds {
		mustInsertRecord(t, ctx, repo, rec)
	}
	scope := memory.Scope{Channel: event.ChLive, Session: "room"}

	// 不带查询时按重要度：低重要度的相关摘要排不进前 3。
	plain, err := repo.Records(ctx, scope, 3)
	if err != nil {
		t.Fatalf("Records 失败：%v", err)
	}
	rankingWant(t, plain, "heavy-a", "heavy-b", "heavy-c")

	// 带查询时相关摘要先出，且高重要度主体仍受配额 3 限制。
	ranked, err := repo.RecordsFor(ctx, scope, "白饭", 3)
	if err != nil {
		t.Fatalf("RecordsFor 失败：%v", err)
	}
	rankingWant(t, ranked, "rel", "heavy-a", "heavy-b")
}

// TestMemoryRecordsSubjectQuotaByFromID 同一 from_id 最多 3 条，其他主体仍能出现。
func TestMemoryRecordsSubjectQuotaByFromID(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ids := []string{"u1-a", "u1-b", "u1-c", "u1-d", "u1-e"}
	for i, id := range ids {
		mustInsertRecord(t, ctx, repo, rankingRecord(id, "主题"+id, "摘要"+id, "uid-1", 0.9-float64(i)*0.01, base))
	}
	mustInsertRecord(t, ctx, repo, rankingRecord("u2-a", "别的主题", "别的摘要", "uid-2", 0.1, base))

	rows, err := repo.Records(ctx, memory.Scope{Channel: event.ChLive, Session: "room"}, 10)
	if err != nil {
		t.Fatalf("Records 失败：%v", err)
	}
	rankingWant(t, rows, "u1-a", "u1-b", "u1-c", "u2-a")
}

// TestMemoryRecordsSubjectQuotaByTitle 没有 from_id 时按主题（title）限额。
func TestMemoryRecordsSubjectQuotaByTitle(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ids := []string{"w1-a", "w1-b", "w1-c", "w1-d", "w1-e"}
	for i, id := range ids {
		mustInsertRecord(t, ctx, repo, rankingRecord(id, "天气", "天气摘要"+id, "", 0.7-float64(i)*0.01, base))
	}
	mustInsertRecord(t, ctx, repo, rankingRecord("other", "别的主题", "别的摘要", "", 0.1, base))

	rows, err := repo.Records(ctx, memory.Scope{Channel: event.ChLive, Session: "room"}, 10)
	if err != nil {
		t.Fatalf("Records 失败：%v", err)
	}
	rankingWant(t, rows, "w1-a", "w1-b", "w1-c", "other")
}

// TestMemoryRecordsRelevanceOrdering 相关性优先，其次重要度；查询词按标点切开也能命中。
func TestMemoryRecordsRelevanceOrdering(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	seeds := []store.MemoryRecord{
		rankingRecord("rel-high", "白饭", "白饭很香", "uid-a", 0.6, base),
		rankingRecord("rel-low", "白饭团", "白饭团也好吃", "uid-b", 0.1, base),
		rankingRecord("irrelevant", "天气", "今天晴天", "uid-c", 0.99, base),
	}
	for _, rec := range seeds {
		mustInsertRecord(t, ctx, repo, rec)
	}

	rows, err := repo.RecordsFor(ctx, memory.Scope{Channel: event.ChLive, Session: "room"}, "白饭，今天吃什么", 10)
	if err != nil {
		t.Fatalf("RecordsFor 失败：%v", err)
	}
	rankingWant(t, rows, "rel-high", "rel-low", "irrelevant")
}

// TestMemoryRecordsQueryKeepsPrivacy 相关性排序不改变公开/私密可见范围。
func TestMemoryRecordsQueryKeepsPrivacy(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	mustInsertRecord(t, ctx, repo, rankingRecord("pub-rel", "白饭", "公开的白饭", "uid-x", 0.5, base))
	private := rankingRecord("priv-rel", "白饭", "私密的白饭", "uid-x", 0.9, base)
	private.Channel = event.ChDM
	private.Session = "u-9"
	private.Private = true
	mustInsertRecord(t, ctx, repo, private)

	publicRows, err := repo.RecordsFor(ctx, memory.Scope{Channel: event.ChLive, Session: "room"}, "白饭", 10)
	if err != nil {
		t.Fatalf("公开召回失败：%v", err)
	}
	rankingWant(t, publicRows, "pub-rel")

	dmRows, err := repo.RecordsFor(ctx, memory.Scope{Channel: event.ChDM, Session: "u-9", Private: true}, "白饭", 10)
	if err != nil {
		t.Fatalf("私密召回失败：%v", err)
	}
	rankingWant(t, dmRows, "priv-rel", "pub-rel")

	// 查询词里的 SQL 元字符只按字面匹配：既不能扩大范围带出私密行，
	// 也不能让「无相关内容」变成空结果——排序偏好不改变可见集合。
	injected, err := repo.RecordsFor(ctx, memory.Scope{Channel: event.ChLive, Session: "room"}, "' OR true --", 10)
	if err != nil {
		t.Fatalf("注入串不应报错：%v", err)
	}
	rankingWant(t, injected, "pub-rel")
}

// TestMemoryRecordsForRejectsBadInput 非法参数在查询前拒绝，超长查询只截断不报错。
func TestMemoryRecordsForRejectsBadInput(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	scope := memory.Scope{Channel: event.ChLive, Session: "room"}

	for _, limit := range []int{0, -1, 101} {
		if _, err := repo.RecordsFor(ctx, scope, "白饭", limit); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("limit=%d 应返回 ErrInvalid，实际 %v", limit, err)
		}
	}
	if _, err := repo.RecordsFor(ctx, memory.Scope{Channel: event.ChDM, Session: "u"}, "白饭", 10); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("无效 scope 应返回 ErrInvalid，实际 %v", err)
	}
	//nolint:staticcheck // 刻意传 nil 验证防御边界（SA1012）
	if _, err := repo.RecordsFor(nil, scope, "白饭", 10); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("nil ctx 应返回 ErrInvalid，实际 %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.RecordsFor(canceled, scope, "白饭", 10); !errors.Is(err, context.Canceled) {
		t.Errorf("取消未保留原因：%v", err)
	}

	long := strings.Repeat("猫", memorySearchMaxRunes+20)
	if utf8.RuneCountInString(long) <= memorySearchMaxRunes {
		t.Fatal("测试查询不够长")
	}
	if _, err := repo.RecordsFor(ctx, scope, long, 10); err != nil {
		t.Errorf("超长查询应截断而不是报错：%v", err)
	}
}
