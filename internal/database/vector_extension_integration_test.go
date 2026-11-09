//go:build integration

package database

// D2/N16：干净数据库首迁必须把 pgvector 装进 public。
//
// 复刻 CI 条件：全新数据库 + 多个随机 schema 并发首迁。
// 随后模拟测试清理（DROP SCHEMA CASCADE），确认数据库级扩展仍完好，
// 再重复一次迁移，证明同一库可以反复执行 CI 命令。

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

func vectorExtensionTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("FISH_TEST_DSN")
	if dsn == "" {
		env, err := godotenv.Read(filepath.Join("..", "..", ".env"))
		if err != nil {
			t.Fatal("集成测试需要 FISH_TEST_DSN 或本地 .env")
		}
		dsn = env["FISH_DB_DSN"]
	}
	if strings.TrimSpace(dsn) == "" {
		t.Fatal("集成测试缺少数据库连接串")
	}
	return dsn
}

// dsnWithDatabase 把连接串指向另一个数据库；只支持仓库实际使用的两种形式。
func dsnWithDatabase(t *testing.T, dsn, name string) string {
	t.Helper()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("测试DSN格式错误")
		}
		u.Path = "/" + name
		return u.String()
	}
	return dsn + " dbname=" + name
}

func randomSchema(t *testing.T, prefix string) string {
	t.Helper()
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s_%x", prefix, suffix)
}

func assertVectorExtensionSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	var schemas []string
	if err := db.Raw("SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'vector'").Scan(&schemas).Error; err != nil {
		t.Fatalf("查询向量扩展失败：%v", err)
	}
	if len(schemas) != 1 || schemas[0] != vectorExtensionSchema {
		t.Fatalf("向量扩展位置 = %v，期望 [%s]", schemas, vectorExtensionSchema)
	}
}

func TestVectorExtensionStableSchemaOnCleanDatabase(t *testing.T) {
	baseDSN := vectorExtensionTestDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	admin, err := Open(ctx, baseDSN, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Close(admin); err != nil {
			t.Error(err)
		}
	})

	dbName := randomSchema(t, "fish_n16")
	quotedDB := pgx.Identifier{dbName}.Sanitize()
	if err := admin.WithContext(ctx).Exec("CREATE DATABASE " + quotedDB).Error; err != nil {
		// 本地开发角色可能没有 CREATEDB；CI 的 fish 是超级用户，验收在那里执行。
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42501" {
			t.Skip("当前数据库角色无 CREATEDB 权限，干净库验收需在 CI 超级用户下执行")
		}
		t.Fatalf("创建干净测试库失败：%v", err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		if err := admin.WithContext(cleanup).Exec("DROP DATABASE " + quotedDB + " WITH (FORCE)").Error; err != nil {
			t.Error("清理干净测试库失败")
		}
	})

	fresh, err := Open(ctx, dsnWithDatabase(t, baseDSN, dbName), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Close(fresh); err != nil {
			t.Error(err)
		}
	})

	// 两个随机 schema 并发首迁：扩展只能有一份，且必须在 public。
	openSchema := func(schema string) *gorm.DB {
		t.Helper()
		quoted := pgx.Identifier{schema}.Sanitize()
		if err := fresh.WithContext(ctx).Exec("CREATE SCHEMA " + quoted).Error; err != nil {
			t.Fatalf("创建测试schema失败：%v", err)
		}
		pc, err := pgx.ParseConfig(dsnWithDatabase(t, baseDSN, dbName))
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
		})
		return db
	}

	schemas := []string{randomSchema(t, "fish_n16_a"), randomSchema(t, "fish_n16_b")}
	dbs := make([]*gorm.DB, len(schemas))
	for i, schema := range schemas {
		dbs[i] = openSchema(schema)
	}
	start := make(chan struct{})
	errs := make(chan error, len(dbs))
	var wg sync.WaitGroup
	for _, db := range dbs {
		wg.Go(func() {
			<-start
			errs <- Migrate(ctx, db)
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发首迁失败：%v", err)
		}
	}
	assertVectorExtensionSchema(t, fresh)
	for i, db := range dbs {
		var resolved bool
		if err := db.Raw("SELECT to_regtype('vector') IS NOT NULL").Scan(&resolved).Error; err != nil || !resolved {
			t.Fatalf("schema %s 解析不到 vector 类型：%v", schemas[i], err)
		}
	}

	// 模拟测试清理：删掉自己的 schema 不能带走数据库级扩展。
	for _, schema := range schemas {
		if err := fresh.WithContext(ctx).Exec("DROP SCHEMA " + pgx.Identifier{schema}.Sanitize() + " CASCADE").Error; err != nil {
			t.Fatalf("清理测试schema失败：%v", err)
		}
	}
	assertVectorExtensionSchema(t, fresh)

	// 同一干净库再跑一轮，证明可以反复执行。
	repeated := openSchema(randomSchema(t, "fish_n16_c"))
	if err := Migrate(ctx, repeated); err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
	assertVectorExtensionSchema(t, fresh)
}
