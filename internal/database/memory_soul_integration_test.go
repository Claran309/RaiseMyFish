//go:build integration

package database

// M12/T08：灵魂候选必须排除未来生效的记录，历史回放不能提前看到未来事实。

import (
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// soulRecord 造一条公开的 fact/preference 候选。
func soulRecord(id, kind, title, summary, fromID, fromName string, at time.Time) store.MemoryRecord {
	return store.MemoryRecord{
		Record: memory.Record{
			ID: id, Channel: event.ChLive, Session: "room", Summary: summary,
			Importance: 0.9, Confidence: 0.9, ValidFrom: at,
		},
		Source: "persona", Kind: kind, Title: title, FromID: fromID, FromName: fromName,
	}
}

// TestSoulCandidatesSkipsFutureFacts 未来 valid_from 的 fact/preference 不进入候选。
func TestSoulCandidatesSkipsFutureFacts(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	past := soulRecord("soul:past", "fact", "主人是学生", "在读大学", "1000", "Claran", now.Add(-2*time.Hour))
	// 显式带上来源分离字段，验证 asserted_by/subject 能往返 PG。
	past.Subject = memory.SoulSubjectOwner
	past.AssertedByID, past.AssertedByName = "1000", "Claran"
	seeds := []store.MemoryRecord{
		past,
		soulRecord("soul:future-fact", "fact", "事实：我是大三学生", "我是大三学生", "u-42", "小明", now.Add(2*time.Hour)),
		soulRecord("soul:future-pref", "preference", "主人相关：明天爱吃甜", "明天开始爱吃甜", "1000", "Claran", now.Add(3*time.Hour)),
	}
	for _, rec := range seeds {
		mustInsertRecord(t, ctx, repo, rec)
	}

	got, err := repo.SoulCandidates(ctx, 0.8, 10)
	if err != nil {
		t.Fatalf("读取灵魂候选失败：%v", err)
	}
	if len(got) != 1 || got[0].ID != "soul:past" {
		t.Fatalf("未来记录不应进入灵魂候选：%+v", got)
	}
	// 主体字段必须原样带出，后续重建才能判断 fish/owner/viewer。
	if got[0].FromID != "1000" || got[0].FromName != "Claran" || got[0].Source != "persona" {
		t.Fatalf("主体/来源字段丢失：%+v", got[0])
	}
	if got[0].Subject != memory.SoulSubjectOwner || got[0].AssertedByID != "1000" || got[0].AssertedByName != "Claran" {
		t.Fatalf("来源分离字段丢失：%+v", got[0])
	}
}
