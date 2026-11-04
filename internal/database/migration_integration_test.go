//go:build integration

package database

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

func isolatedDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("FISH_TEST_DSN")
	if dsn == "" {
		env, err := godotenv.Read(filepath.Join("..", "..", ".env"))
		if err != nil {
			t.Fatal("集成测试需要 FISH_TEST_DSN 或本地 .env")
		}
		dsn = env["FISH_DB_DSN"]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := Open(ctx, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fish_test_%x", suffix)
	quoted := pgx.Identifier{schema}.Sanitize()
	if err := admin.WithContext(ctx).Exec("CREATE SCHEMA " + quoted).Error; err != nil {
		t.Fatal("创建独立测试schema失败")
	}
	pc, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("测试DSN格式错误")
	}
	pc.RuntimeParams["search_path"] = schema + ",public"
	db, err := openConfig(ctx, pc, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Close(db); err != nil {
			t.Error(err)
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := admin.WithContext(cleanup).Exec("DROP SCHEMA " + quoted + " CASCADE").Error; err != nil {
			t.Error("清理测试schema失败")
		}
		if err := Close(admin); err != nil {
			t.Error(err)
		}
	})
	return db
}

func TestMigrationRealPostgres(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Ready(ctx, db); err == nil {
		t.Fatal("空schema不能就绪")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { errs <- Migrate(ctx, db) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Ready(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"events", "tasks", "memory_sources", "memory_records", "llm_usage", "send_log", "dlq", "schema_migrations", "knowledge_embeddings"} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("缺表 %s", table)
		}
	}
	var count int64
	if err := db.WithContext(ctx).Table("memory_sources").Count(&count).Error; err != nil || count != 6 {
		t.Fatalf("来源seed错误：%d %v", count, err)
	}
	if err := db.WithContext(ctx).Exec("INSERT INTO tasks(id,event_id,channel,session,brain) VALUES ('missing','missing','live','room','openai')").Error; err == nil {
		t.Fatal("外键未生效")
	}
	for _, index := range []string{"idx_events_status", "idx_events_session", "idx_events_private", "idx_tasks_status", "uq_tasks_event", "idx_mem_valid", "idx_mem_channel", "knowledge_embeddings_vec_idx"} {
		var exists bool
		if err := db.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND indexname=?)", index).Scan(&exists).Error; err != nil || !exists {
			t.Fatalf("缺索引 %s", index)
		}
	}
	if err := db.WithContext(ctx).Exec("UPDATE schema_migrations SET checksum='tampered'").Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("篡改checksum未被拒绝")
	}
	if err := Ready(ctx, db); err == nil {
		t.Fatal("被篡改迁移不能就绪")
	}
}

func TestMigrationRollbackOnConflict(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.WithContext(ctx).Exec("CREATE TABLE tasks (id text)").Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("既有同名表必须拒绝")
	}
	if db.Migrator().HasTable("events") || db.Migrator().HasTable("schema_migrations") {
		t.Fatal("失败迁移没有整体回滚")
	}
	if !db.Migrator().HasTable("tasks") {
		t.Fatal("既有表被破坏")
	}
}
