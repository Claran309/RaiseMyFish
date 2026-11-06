//go:build integration

package database

// 本文件用真实 PG 测观众档案语义检索：向量入库、按描述找人、降级与增量。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
)

// viewerVector 造一个第 idx 位为 1 的 1024 维测试向量。
func viewerVector(idx int) []float64 {
	vector := make([]float64, viewer.EmbeddingDimension)
	vector[idx%len(vector)] = 1
	return vector
}

// fakeViewerEmbedder 按输入文本内容返回固定向量，模拟语义相近。
type fakeViewerEmbedder struct {
	err   error
	calls int
}

func (f *fakeViewerEmbedder) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	vectors := make([][]float64, len(inputs))
	for i, text := range inputs {
		switch {
		case strings.Contains(text, "白饭"):
			vectors[i] = viewerVector(0)
		case strings.Contains(text, "半夜"):
			vectors[i] = viewerVector(1)
		default:
			vectors[i] = viewerVector(2)
		}
	}
	return vectors, nil
}

// seedViewerProfiles 写入三分档案：白饭哥、夜猫子、路人。
func seedViewerProfiles(t *testing.T, ctx context.Context, repo *ViewerRepo) {
	t.Helper()
	seen := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	profiles := []store.ViewerProfile{
		{UID: "2002", Name: "白饭哥", Hits: 5, Replies: 2, LastSeen: seen, RecentTexts: []string{"今天也要白饭", "再来一碗"}},
		{UID: "3003", Name: "夜猫子", Hits: 3, Replies: 1, LastSeen: seen, RecentTexts: []string{"半夜吃播开始了"}},
		{UID: "4004", Name: "路人", Hits: 2, Replies: 0, LastSeen: seen, RecentTexts: []string{"路过看看"}},
	}
	for _, profile := range profiles {
		if err := repo.UpsertProfile(ctx, profile); err != nil {
			t.Fatalf("写入档案 %s 失败：%v", profile.UID, err)
		}
	}
}

// TestViewerQuestionShellFindsNickname 「弓长三益是谁」也要按昵称命中真实档案。
func TestViewerQuestionShellFindsNickname(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 11, 5, 21, 54, 0, 0, time.UTC)
	profile := store.ViewerProfile{
		UID: "5005", Name: "弓长三益", Hits: 12, Replies: 0,
		LastSeen: seen, RecentTexts: []string{"打错了很正常", "今晚也在"},
	}
	if err := repo.UpsertProfile(ctx, profile); err != nil {
		t.Fatalf("写入档案失败：%v", err)
	}

	searcher, err := viewer.NewSearcher(repo, nil, viewer.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := searcher.Search(ctx, "弓长三益是谁", 3)
	if err != nil {
		t.Fatalf("问句检索失败：%v", err)
	}
	if len(hits) == 0 || hits[0].Profile.UID != "5005" || hits[0].Profile.Hits != 12 {
		t.Fatalf("问句未命中档案：%+v", hits)
	}
}

// TestViewerSemanticSearchFindsUID 「那个爱要白饭的人」→ 找到 2002。
func TestViewerSemanticSearchFindsUID(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seedViewerProfiles(t, ctx, repo)

	embedder := &fakeViewerEmbedder{}
	stats, err := viewer.RunEmbedding(ctx, repo, embedder, viewer.EmbedOptions{BatchSize: 2})
	if err != nil {
		t.Fatalf("档案入库失败：%v", err)
	}
	if stats.Embedded != 3 || stats.Pending != 3 {
		t.Fatalf("档案入库统计不符：%+v", stats)
	}

	searcher, err := viewer.NewSearcher(repo, embedder, viewer.SearchOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := searcher.Search(ctx, "那个爱要白饭的人", 3)
	if err != nil {
		t.Fatalf("语义检索失败：%v", err)
	}
	if len(hits) == 0 || hits[0].Profile.UID != "2002" {
		t.Fatalf("应首先找到 2002：%+v", hits)
	}
	if hits[0].Similarity < 0.99 {
		t.Errorf("相似度 = %v，期望接近 1", hits[0].Similarity)
	}
}

// TestViewerHybridAndDegrade 关键词+语义合并；embedding 失败降级关键词。
func TestViewerHybridAndDegrade(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seedViewerProfiles(t, ctx, repo)
	if _, err := viewer.RunEmbedding(ctx, repo, &fakeViewerEmbedder{}, viewer.EmbedOptions{}); err != nil {
		t.Fatalf("档案入库失败：%v", err)
	}

	searcher, err := viewer.NewSearcher(repo, &fakeViewerEmbedder{}, viewer.SearchOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := searcher.Search(ctx, "白饭", 3)
	if err != nil {
		t.Fatalf("混合检索失败：%v", err)
	}
	if len(hits) == 0 || hits[0].Profile.UID != "2002" || hits[0].Score < 1.4 {
		t.Fatalf("两路命中应排第一：%+v", hits)
	}

	var degraded error
	failing, err := viewer.NewSearcher(repo, &fakeViewerEmbedder{err: errors.New("embedding 不可用")}, viewer.SearchOptions{
		Alpha:           0.5,
		OnSemanticError: func(err error) { degraded = err },
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, err = failing.Search(ctx, "白饭", 3)
	if err != nil {
		t.Fatalf("降级不应报错：%v", err)
	}
	if degraded == nil {
		t.Error("应触发降级回调")
	}
	if len(hits) != 1 || hits[0].Profile.UID != "2002" {
		t.Fatalf("降级后应只靠关键词命中 2002：%+v", hits)
	}
}

// TestViewerEmbeddingIncremental 二次入库全部跳过。
func TestViewerEmbeddingIncremental(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seedViewerProfiles(t, ctx, repo)
	embedder := &fakeViewerEmbedder{}
	stats, err := viewer.RunEmbedding(ctx, repo, embedder, viewer.EmbedOptions{})
	if err != nil {
		t.Fatalf("首次入库失败：%v", err)
	}
	if stats.Embedded != 3 {
		t.Fatalf("首次入库统计不符：%+v", stats)
	}
	stats, err = viewer.RunEmbedding(ctx, repo, embedder, viewer.EmbedOptions{})
	if err != nil {
		t.Fatalf("二次入库失败：%v", err)
	}
	if stats.Skipped != 3 || stats.Pending != 0 || stats.Embedded != 0 {
		t.Fatalf("二次入库未全部跳过：%+v", stats)
	}
}

// TestViewerIdentityTiersRealPostgres I01/I02：真实 PG 下验证身份档位与近似召回。
//
// 关键区别全在 SQL 之外：KeywordProfiles 只能证明「出现过」，
// 昵称精确/UID 精确/编辑距离近似要靠 viewer 层归因。
func TestViewerIdentityTiersRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	for _, profile := range []store.ViewerProfile{
		{UID: "5005", Name: "弓长三益", Hits: 12, LastSeen: seen, RecentTexts: []string{"今晚也在"}},
		{UID: "5006", Name: "路人甲", Hits: 3, LastSeen: seen, RecentTexts: []string{"昨天弓长三益说了个笑话"}},
		{UID: "5007", Name: "今夜琴声温柔", Hits: 2, LastSeen: seen, RecentTexts: []string{"这歌好听"}},
	} {
		if err := repo.UpsertProfile(ctx, profile); err != nil {
			t.Fatalf("写入档案失败：%v", err)
		}
	}
	searcher, err := viewer.NewSearcher(repo, nil, viewer.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// UID 精确：纯数字直接命中，档位最高。
	got, err := searcher.Find(ctx, "5005", 5)
	if err != nil {
		t.Fatalf("UID 精确查询失败：%v", err)
	}
	if got.Status != viewer.StatusFound || len(got.Candidates) != 1 || got.Candidates[0].Tier != viewer.TierUID {
		t.Fatalf("UID 精确档位不符：%+v", got)
	}

	// 昵称精确压过「发言里提到」。
	got, err = searcher.Find(ctx, "弓长三益", 5)
	if err != nil {
		t.Fatalf("昵称查询失败：%v", err)
	}
	if got.Status != viewer.StatusFound || len(got.Candidates) < 2 {
		t.Fatalf("应返回昵称命中与内容命中：%+v", got)
	}
	if got.Candidates[0].Profile.UID != "5005" || got.Candidates[0].Tier != viewer.TierName {
		t.Fatalf("昵称精确应排第一：%+v", got.Candidates)
	}
	if got.Candidates[1].Profile.UID != "5006" || got.Candidates[1].Tier != viewer.TierContent {
		t.Fatalf("提到名字的人应落在内容档：%+v", got.Candidates)
	}

	// 错字近似：SQL 的 ILIKE 不命中，靠活跃昵称池的编辑距离找到。
	got, err = searcher.Find(ctx, "今夜琴身温柔", 5)
	if err != nil {
		t.Fatalf("近似查询失败：%v", err)
	}
	if got.Status != viewer.StatusFound || len(got.Candidates) == 0 {
		t.Fatalf("近似未命中：%+v", got)
	}
	if got.Candidates[0].Profile.UID != "5007" || got.Candidates[0].Tier != viewer.TierNear {
		t.Fatalf("近似档位不符：%+v", got.Candidates[0])
	}
}
