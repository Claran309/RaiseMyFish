//go:build integration

package infra

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

	"RaiseMyFish/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func TestSetupSingletonAndClose(t *testing.T) {
	dsn := os.Getenv("FISH_TEST_DSN")
	if dsn == "" {
		env, err := godotenv.Read(filepath.Join("..", "..", ".env"))
		if err != nil {
			t.Fatal("需要真实PG配置")
		}
		dsn = env["FISH_DB_DSN"]
	}
	cfg := config.Config{Database: config.Database{DSN: dsn, TimeoutSeconds: 5}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := Setup(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fish_lock_%x", suffix)
	quoted := pgx.Identifier{schema}.Sanitize()
	if err := admin.DB.WithContext(ctx).Exec("CREATE SCHEMA " + quoted).Error; err != nil {
		t.Fatal("创建测试schema失败")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if err := admin.DB.WithContext(cleanup).Exec("DROP SCHEMA " + quoted + " CASCADE").Error; err != nil {
			t.Error("清理测试schema失败")
		}
		if err := admin.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("测试DSN格式错误")
		}
		q := u.Query()
		q.Set("search_path", schema+",public")
		u.RawQuery = q.Encode()
		cfg.Database.DSN = u.String()
	} else {
		cfg.Database.DSN = dsn + " search_path=" + schema + ",public"
	}
	one, err := Setup(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := one.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	two, err := Setup(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := two.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	if err := one.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := two.Acquire(ctx); err == nil {
		t.Fatal("允许了第二个fishd")
	}
	canceled, cancelClose := context.WithCancel(ctx)
	cancelClose()
	if err := one.Close(canceled); err != nil {
		t.Fatal(err)
	}
	if err := two.Acquire(ctx); err != nil {
		t.Fatal("关闭后单实例锁未释放")
	}
	if err := two.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}
