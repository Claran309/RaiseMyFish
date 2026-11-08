//go:build integration

package database

// 本文件用真实 PostgreSQL 测 O10：删除/激活的竞态防护与静默失败。
//
// 核心不变式：accounts 表任何时刻最多一个活跃账号（部分唯一索引），
// 且「删除活跃账号」必须被拒绝、「激活失败」不能把旧活跃弄丢。

import (
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/account"
)

// TestAccountDeleteRejectsActive O10：活跃账号不能被删除，判定在 DELETE 的 WHERE 里。
func TestAccountDeleteRejectsActive(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewAccountRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	active := account.Account{
		ID: account.IDForMID("5001"), MID: "5001", Name: "活跃号",
		Cookie: "SESSDATA=a; bili_jct=c1", CSRF: "c1", Status: account.StatusActive, LastCheck: now,
	}
	if _, err := repo.Upsert(ctx, active); err != nil {
		t.Fatal(err)
	}
	spare := account.Account{
		ID: account.IDForMID("5002"), MID: "5002", Name: "备用号",
		Cookie: "SESSDATA=b; bili_jct=c2", CSRF: "c2", Status: account.StatusActive, LastCheck: now,
	}
	if _, err := repo.Upsert(ctx, spare); err != nil {
		t.Fatal(err)
	}

	// 删活跃：拒绝，且行必须还在（这就是旧实现「检查后被 Activate 插队」会删掉的东西）。
	if err := repo.Delete(ctx, active.ID); !errors.Is(err, account.ErrActiveAccount) {
		t.Fatalf("删除活跃账号应 ErrActiveAccount：%v", err)
	}
	if _, err := repo.Get(ctx, active.ID); err != nil {
		t.Fatalf("活跃账号被误删：%v", err)
	}
	// 活跃标记没有被条件删除顺手清掉。
	if got, err := repo.Active(ctx); err != nil || got.ID != active.ID {
		t.Fatalf("活跃账号不应变化：%+v %v", got, err)
	}

	// 删非活跃：成功。
	if err := repo.Delete(ctx, spare.ID); err != nil {
		t.Fatalf("删除非活跃账号失败：%v", err)
	}
	if _, err := repo.Get(ctx, spare.ID); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("非活跃账号应已删除：%v", err)
	}

	// 删不存在：ErrNotFound。
	if err := repo.Delete(ctx, "missing"); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("删除不存在应 ErrNotFound：%v", err)
	}
}

// TestAccountSetActiveKeepsSingleActive O10：激活成功必须恰好一个活跃，失败不能丢旧活跃。
func TestAccountSetActiveKeepsSingleActive(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewAccountRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	first := account.Account{
		ID: account.IDForMID("6001"), MID: "6001", Name: "一号",
		Cookie: "SESSDATA=a; bili_jct=c1", CSRF: "c1", Status: account.StatusActive, LastCheck: now,
	}
	second := account.Account{
		ID: account.IDForMID("6002"), MID: "6002", Name: "二号",
		Cookie: "SESSDATA=b; bili_jct=c2", CSRF: "c2", Status: account.StatusActive, LastCheck: now,
	}
	for _, a := range []account.Account{first, second} {
		if _, err := repo.Upsert(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	assertSingleActive := func(wantID string) {
		t.Helper()
		var rows []struct {
			ID string `gorm:"column:id"`
		}
		if err := db.WithContext(ctx).Raw("SELECT id FROM accounts WHERE is_active").Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ID != wantID {
			t.Fatalf("活跃账号应恰好是 %s，实际 %+v", wantID, rows)
		}
	}

	// 切到不存在的账号：返回 ErrNotFound，旧活跃必须原样保留。
	if err := repo.SetActive(ctx, "missing"); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("切换不存在账号应 ErrNotFound：%v", err)
	}
	assertSingleActive(first.ID)

	// 正常切换：唯一活跃且是目标。
	if err := repo.SetActive(ctx, second.ID); err != nil {
		t.Fatalf("切换失败：%v", err)
	}
	assertSingleActive(second.ID)

	// 切回：仍然唯一。
	if err := repo.SetActive(ctx, first.ID); err != nil {
		t.Fatalf("切回失败：%v", err)
	}
	assertSingleActive(first.ID)

	// 目标在检查之后消失（等价于并发删除）：不能留下「没有活跃账号」。
	if err := repo.Delete(ctx, second.ID); err != nil {
		t.Fatalf("删除非活跃账号失败：%v", err)
	}
	if err := repo.SetActive(ctx, second.ID); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("切换到已删除账号应 ErrNotFound：%v", err)
	}
	assertSingleActive(first.ID)
}
