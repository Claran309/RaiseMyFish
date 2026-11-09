//go:build integration

package database

// 本文件用真实 PG 测观众档案语义检索：向量入库、按描述找人、降级与增量。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	stats, err := viewer.RunEmbedding(ctx, repo, embedder, viewer.EmbedOptions{BatchSize: 2, Fingerprint: testIndexFingerprint})
	if err != nil {
		t.Fatalf("档案入库失败：%v", err)
	}
	// 3 份档案各有 identity + facts 两路，共 6 份向量文档。
	if stats.Embedded != 6 || stats.Pending != 6 {
		t.Fatalf("档案入库统计不符：%+v", stats)
	}

	searcher, err := viewer.NewSearcher(repo, embedder, viewer.SearchOptions{Alpha: 0.5, Fingerprint: testIndexFingerprint})
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

// TestViewerIndexesIsolatedByKind M10：身份/本人事实/互动三路互不污染。
//
// 关键回归：鱼对 6001 说过「白饭」，但 6001 本人没说过白饭——
// 认人时不能把鱼的话当成观众特点，facts/interaction 必须各查各的索引。
func TestViewerIndexesIsolatedByKind(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, profile := range []store.ViewerProfile{
		{UID: "6001", Name: "路人甲", Hits: 2, Replies: 1, LastSeen: seen,
			RecentTexts: []string{"今天天气不错"}, RecentReplies: []string{"白饭真香"}},
		{UID: "6002", Name: "熟客乙", Hits: 2, Replies: 0, LastSeen: seen,
			RecentTexts: []string{"我爱白饭"}},
	} {
		if err := repo.UpsertProfile(ctx, profile); err != nil {
			t.Fatalf("写入档案 %s 失败：%v", profile.UID, err)
		}
	}
	if _, err := viewer.RunEmbedding(ctx, repo, &fakeViewerEmbedder{}, viewer.EmbedOptions{Fingerprint: testIndexFingerprint}); err != nil {
		t.Fatalf("档案入库失败：%v", err)
	}

	// 「白饭」= 第 0 维；identity 两路（路人甲/熟客乙）都落在第 2 维，与查询正交。
	vector := viewerVector(0)
	relevantUIDs := func(rows []store.ViewerProfileHit) string {
		t.Helper()
		var uids []string
		for _, row := range rows {
			if row.Similarity >= 0.99 {
				uids = append(uids, row.Profile.UID)
			}
		}
		return strings.Join(uids, ",")
	}
	semantic := func(kind viewer.EmbeddingKind) string {
		t.Helper()
		rows, err := repo.SemanticProfiles(ctx, kind, vector, testIndexFingerprint, 10)
		if err != nil {
			t.Fatalf("%s 向量检索失败：%v", kind, err)
		}
		return relevantUIDs(rows)
	}
	if got := semantic(viewer.KindFacts); got != "6002" {
		t.Errorf("facts 向量应只命中说过白饭的 6002：%q", got)
	}
	if got := semantic(viewer.KindInteraction); got != "6001" {
		t.Errorf("interaction 向量应只命中被鱼回复过的 6001：%q", got)
	}
	if got := semantic(viewer.KindIdentity); got != "" {
		t.Errorf("identity 向量不应命中昵称无关的人：%q", got)
	}

	keyword := func(kind viewer.EmbeddingKind) string {
		t.Helper()
		rows, err := repo.KeywordProfiles(ctx, kind, "白饭", 10)
		if err != nil {
			t.Fatalf("%s 关键词检索失败：%v", kind, err)
		}
		var uids []string
		for _, row := range rows {
			uids = append(uids, row.Profile.UID)
		}
		return strings.Join(uids, ",")
	}
	if got := keyword(viewer.KindFacts); got != "6002" {
		t.Errorf("facts 关键词应只命中 6002：%q", got)
	}
	if got := keyword(viewer.KindIdentity); got != "" {
		t.Errorf("identity 关键词不应命中 6001/6002：%q", got)
	}
	if got := keyword(viewer.KindInteraction); got != "6001" {
		t.Errorf("interaction 关键词应只命中 6001：%q", got)
	}

	// 认人链路：鱼的「白饭真香」不能把 6001 拉成候选。
	searcher, err := viewer.NewSearcher(repo, &fakeViewerEmbedder{}, viewer.SearchOptions{Alpha: 0.5, Fingerprint: testIndexFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := searcher.Search(ctx, "白饭", 10)
	if err != nil {
		t.Fatalf("认人检索失败：%v", err)
	}
	var uids []string
	for _, hit := range hits {
		uids = append(uids, hit.Profile.UID)
	}
	if got := strings.Join(uids, ","); got != "6002" {
		t.Fatalf("认人不应被鱼的回复污染：%q", got)
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
	if _, err := viewer.RunEmbedding(ctx, repo, &fakeViewerEmbedder{}, viewer.EmbedOptions{Fingerprint: testIndexFingerprint}); err != nil {
		t.Fatalf("档案入库失败：%v", err)
	}

	searcher, err := viewer.NewSearcher(repo, &fakeViewerEmbedder{}, viewer.SearchOptions{Alpha: 0.5, Fingerprint: testIndexFingerprint})
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
		Fingerprint:     testIndexFingerprint,
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
	stats, err := viewer.RunEmbedding(ctx, repo, embedder, viewer.EmbedOptions{Fingerprint: testIndexFingerprint})
	if err != nil {
		t.Fatalf("首次入库失败：%v", err)
	}
	if stats.Embedded != 6 {
		t.Fatalf("首次入库统计不符：%+v", stats)
	}
	stats, err = viewer.RunEmbedding(ctx, repo, embedder, viewer.EmbedOptions{Fingerprint: testIndexFingerprint})
	if err != nil {
		t.Fatalf("二次入库失败：%v", err)
	}
	if stats.Skipped != 9 || stats.Pending != 0 || stats.Embedded != 0 {
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

// TestViewerUnknownUIDUnifiedNotFoundRealPostgres B3/N04：真实仓储下陌生 UID 是 not_found。
//
// 关键回归：GetProfile 过去只返回 store.ErrNotFound，而检索层识别的是
// 独立的 ErrViewerNotFound，导致新观众精确 UID 未命中被包成工具错误。
func TestViewerUnknownUIDUnifiedNotFoundRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	_, err = repo.GetProfile(ctx, "999999")
	if err == nil {
		t.Fatal("未建档 UID 应返回错误")
	}
	if !errors.Is(err, store.ErrViewerNotFound) {
		t.Errorf("仓储应返回 ErrViewerNotFound：%v", err)
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("同时应满足通用 ErrNotFound 契约：%v", err)
	}

	searcher, err := viewer.NewSearcher(repo, nil, viewer.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := searcher.Find(ctx, "999999", 5)
	if err != nil {
		t.Fatalf("陌生 UID 查询不能变成错误：%v", err)
	}
	if got.Status != viewer.StatusNotFound || len(got.Candidates) != 0 {
		t.Fatalf("陌生 UID 应为 not_found：%+v", got)
	}
}

// TestViewerAliasFileInjectionRealPostgres B3/N04：真实仓储 + 别名文件，生产注入生效。
func TestViewerAliasFileInjectionRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, profile := range []store.ViewerProfile{
		{UID: "5105", Name: "弓长三益", Hits: 6, LastSeen: seen, RecentTexts: []string{"今晚也在"}},
		{UID: "5106", Name: "真得", Hits: 4, LastSeen: seen, RecentTexts: []string{"打错了很正常"}},
	} {
		if err := repo.UpsertProfile(ctx, profile); err != nil {
			t.Fatalf("写入档案 %s 失败：%v", profile.UID, err)
		}
	}

	path := filepath.Join(t.TempDir(), "viewer_aliases.yaml")
	doc := `schema: fish-viewer-aliases/v1
aliases:
  - alias: 长弓三溢
    uid: "5105"
    status: confirmed
  - alias: 长弓三亿
    uid: "5105"
    status: candidate
phonetic_groups:
  - words: ["贞德", "真得"]
    status: confirmed
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("写入别名文件失败：%v", err)
	}
	aliasFile, err := viewer.NewAliasFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	searcher, err := viewer.NewSearcher(repo, nil, viewer.SearchOptions{AliasSource: aliasFile})
	if err != nil {
		t.Fatal(err)
	}

	got, err := searcher.Find(ctx, "长弓三溢", 5)
	if err != nil {
		t.Fatalf("别名查询失败：%v", err)
	}
	if got.Status != viewer.StatusFound || len(got.Candidates) == 0 {
		t.Fatalf("confirmed 别名应命中：%+v", got)
	}
	if got.Candidates[0].Profile.UID != "5105" || got.Candidates[0].Tier != viewer.TierAlias {
		t.Fatalf("别名档位不符：%+v", got.Candidates[0])
	}

	got, err = searcher.Find(ctx, "长弓三亿", 5)
	if err != nil {
		t.Fatalf("候选别名查询失败：%v", err)
	}
	if got.Status != viewer.StatusNotFound {
		t.Fatalf("candidate 别名不应生效：%+v", got)
	}

	got, err = searcher.Find(ctx, "贞德", 5)
	if err != nil {
		t.Fatalf("同音查询失败：%v", err)
	}
	if got.Status != viewer.StatusFound || len(got.Candidates) == 0 {
		t.Fatalf("同音组应命中：%+v", got)
	}
	if got.Candidates[0].Profile.UID != "5106" || got.Candidates[0].Tier != viewer.TierNear ||
		got.Candidates[0].Reason != "phonetic" {
		t.Fatalf("同音档位不符：%+v", got.Candidates[0])
	}
}
