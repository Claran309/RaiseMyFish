//go:build integration

package main

// M08 真实 PG 验收：定时增量刷新把「没有向量」的记忆/档案补算出来，第二轮不重复。
//
// 与知识库刷新同口径：真实 PG + 假 embedder，不调用外部向量服务。

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// embeddingTestDB 建一个独立 schema 并迁移，返回连接与 ctx。
func embeddingTestDB(t *testing.T) (*gorm.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("FISH_TEST_DSN")
	if dsn == "" {
		env, err := godotenv.Read(filepath.Join("..", "..", ".env"))
		if err != nil {
			t.Fatal("集成测试需要 FISH_TEST_DSN 或本地 .env")
		}
		dsn = env["FISH_DB_DSN"]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	admin, err := database.Open(ctx, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fish_vec_%x", suffix)
	quoted := pgx.Identifier{schema}.Sanitize()
	if err := admin.WithContext(ctx).Exec("CREATE SCHEMA " + quoted).Error; err != nil {
		t.Fatal("创建测试schema失败")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		if err := admin.WithContext(cleanup).Exec("DROP SCHEMA " + quoted + " CASCADE").Error; err != nil {
			t.Error("清理测试schema失败")
		}
		if err := database.Close(admin); err != nil {
			t.Error(err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("DSN格式错误")
		}
		q := u.Query()
		q.Set("search_path", schema+",public")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema + ",public"
	}
	db, err := database.Open(ctx, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	return db, ctx
}

// TestVectorRefreshAgainstRealPostgres 记忆与档案的向量都该被补齐，且第二轮跳过。
func TestVectorRefreshAgainstRealPostgres(t *testing.T) {
	db, ctx := embeddingTestDB(t)
	memRepo, err := database.NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	vwRepo, err := database.NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := memRepo.InsertRecord(ctx, store.MemoryRecord{
		Record: memory.Record{
			ID: "fact:public:2002:vec1", Channel: event.ChLive, Session: "room",
			Summary: "我今年大二，学计算机", Importance: 0.6, Confidence: 0.85, ValidFrom: now,
		},
		Source: "persona", Kind: "fact", Title: "事实：大二学生", FromID: "2002", FromName: "熟客乙",
	}); err != nil {
		t.Fatalf("写入记忆失败：%v", err)
	}
	if err := vwRepo.UpsertProfile(ctx, store.ViewerProfile{
		UID: "2002", Name: "白饭哥", Hits: 5, Replies: 2, LastSeen: now,
		RecentTexts: []string{"今天也要白饭"},
	}); err != nil {
		t.Fatalf("写入档案失败：%v", err)
	}

	emb := &fakeVectorEmbedder{}
	r := &memoryEmbeddingRefresher{
		memoryStore:       memRepo,
		viewerStore:       vwRepo,
		embedder:          emb,
		batchSize:         32,
		memoryFingerprint: testMemoryFingerprint,
		viewerFingerprint: testViewerFingerprint,
		logger:            zap.NewNop(),
	}

	r.refreshOnce(ctx)
	// 记忆 1 条 + 档案 identity/facts 两路（没有回复 → 无 interaction）= 3。
	if emb.count() != 3 {
		t.Fatalf("首轮应向量化 3 条（记忆 + identity + facts），实际 %d", emb.count())
	}

	var memEmbedded bool
	if err := db.WithContext(ctx).Raw(
		"SELECT embedding IS NOT NULL FROM memory_records WHERE id = ?", "fact:public:2002:vec1").Scan(&memEmbedded).Error; err != nil {
		t.Fatal(err)
	}
	if !memEmbedded {
		t.Fatal("记忆向量未写入")
	}
	// M10：认人用 identity、查发言用 facts，两路都必须独立落库。
	var identityEmbedded, factsEmbedded, interactionEmbedded bool
	if err := db.WithContext(ctx).Raw(
		"SELECT identity_embedding IS NOT NULL, facts_embedding IS NOT NULL, interaction_embedding IS NOT NULL FROM viewer_profiles WHERE uid = ?",
		"2002").Row().Scan(&identityEmbedded, &factsEmbedded, &interactionEmbedded); err != nil {
		t.Fatal(err)
	}
	if !identityEmbedded || !factsEmbedded {
		t.Fatalf("档案 identity/facts 向量未写入：identity=%v facts=%v", identityEmbedded, factsEmbedded)
	}
	if interactionEmbedded {
		t.Fatal("没有回复时不该产生 interaction 向量")
	}

	// 第二轮：内容没变，不应再调用 embedding。
	r.refreshOnce(ctx)
	if emb.count() != 3 {
		t.Fatalf("第二轮不应重复向量化：累计 %d 次", emb.count())
	}

	// 内容变化后必须重算（hash 比较生效）。
	if _, err := memRepo.InsertRecord(ctx, store.MemoryRecord{
		Record: memory.Record{
			ID: "fact:public:2002:vec1", Channel: event.ChLive, Session: "room",
			Summary: "我今年大三，转去学医信", Importance: 0.7, Confidence: 0.85, ValidFrom: now.Add(time.Second),
		},
		Source: "persona", Kind: "fact", Title: "事实：大二学生", FromID: "2002", FromName: "熟客乙",
	}); err != nil {
		t.Fatalf("更新记忆失败：%v", err)
	}
	r.refreshOnce(ctx)
	if emb.count() != 4 {
		t.Fatalf("内容变化应重算一次：累计 %d 次", emb.count())
	}
}
