//go:build integration

package database

// 本文件用真实 PostgreSQL 测 accounts 仓储：幂等写入、唯一活跃、切换与验证更新。

import (
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/account"
	"RaiseMyFish/internal/store"
)

func TestAccountRepoRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewAccountRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	var _ account.Store = repo

	now := time.Date(2026, 11, 5, 12, 0, 0, 123000000, time.UTC)
	first := account.Account{
		ID: account.IDForMID("1001"), MID: "1001", Name: "一号",
		Cookie: "SESSDATA=first; bili_jct=csrf-1", CSRF: "csrf-1",
		Status: account.StatusActive, LastCheck: now, Note: "主号",
	}
	got, err := repo.Upsert(ctx, first)
	if err != nil {
		t.Fatalf("写入首个账号失败：%v", err)
	}
	if !got.IsActive || got.ID != first.ID || got.Cookie != first.Cookie || got.CSRF != "csrf-1" {
		t.Fatalf("首个账号未自动激活或字段丢失：%+v", got)
	}
	if !got.LastCheck.Equal(now) || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("时间字段不符：%+v", got)
	}

	// 同 mid 幂等：更新 cookie/名称，空 note 不覆盖
	first.Name = "一号改名"
	first.Cookie = "SESSDATA=first-new; bili_jct=csrf-1b"
	first.CSRF = "csrf-1b"
	first.LastCheck = now.Add(time.Minute)
	got, err = repo.Upsert(ctx, first)
	if err != nil {
		t.Fatalf("幂等写入失败：%v", err)
	}
	if got.ID != first.ID || got.Name != "一号改名" || got.Cookie != first.Cookie || got.Note != "主号" || !got.IsActive {
		t.Fatalf("幂等更新不符：%+v", got)
	}
	if !got.LastCheck.Equal(now.Add(time.Minute)) {
		t.Fatalf("last_check 未更新：%v", got.LastCheck)
	}

	second := account.Account{
		ID: account.IDForMID("2002"), MID: "2002", Name: "二号",
		Cookie: "SESSDATA=second; bili_jct=csrf-2", CSRF: "csrf-2",
		Status: account.StatusActive, Note: "备用",
	}
	if _, err := repo.Upsert(ctx, second); err != nil {
		t.Fatalf("写入第二个账号失败：%v", err)
	}

	list, err := repo.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("列表不符：%v %v", list, err)
	}
	if list[0].ID != first.ID || !list[0].IsActive || list[1].ID != second.ID || list[1].IsActive {
		t.Fatalf("列表顺序/活跃标记不符：%+v", list)
	}

	active, err := repo.Active(ctx)
	if err != nil || active.ID != first.ID {
		t.Fatalf("活跃账号不符：%+v %v", active, err)
	}

	if err := repo.SetActive(ctx, second.ID); err != nil {
		t.Fatalf("切换失败：%v", err)
	}
	active, err = repo.Active(ctx)
	if err != nil || active.ID != second.ID {
		t.Fatalf("切换后活跃不符：%+v %v", active, err)
	}
	list, _ = repo.List(ctx)
	if list[0].ID != second.ID || !list[0].IsActive || list[1].ID != first.ID || list[1].IsActive {
		t.Fatalf("切换后列表不符：%+v", list)
	}

	// 部分唯一索引：已有活跃账号时直接插入第二个活跃必须失败
	if err := db.WithContext(ctx).Exec(
		"INSERT INTO accounts(id,mid,name,cookie,csrf,is_active) VALUES ('dup','3003','三号','SESSDATA=x','',TRUE)").Error; err == nil {
		t.Fatal("唯一活跃索引未生效")
	}

	if err := repo.SetActive(ctx, "missing"); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("切换不存在账号应 ErrNotFound：%v", err)
	}

	checkAt := now.Add(2 * time.Minute)
	if err := repo.UpdateCheck(ctx, second.ID, "二号改名", account.StatusInvalid, checkAt); err != nil {
		t.Fatalf("更新验证状态失败：%v", err)
	}
	stored, err := repo.Get(ctx, second.ID)
	if err != nil || stored.Name != "二号改名" || stored.Status != account.StatusInvalid || !stored.LastCheck.Equal(checkAt) {
		t.Fatalf("验证状态未持久化：%+v %v", stored, err)
	}

	if err := repo.UpdateCheck(ctx, "missing", "x", account.StatusActive, checkAt); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("更新不存在账号应 ErrNotFound：%v", err)
	}
	if _, err := repo.Get(ctx, "missing' OR true --"); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("注入串应查无账号：%v", err)
	}

	if err := repo.Delete(ctx, first.ID); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if _, err := repo.Get(ctx, first.ID); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("删除后仍可读取：%v", err)
	}
	if err := repo.Delete(ctx, "missing"); !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("删除不存在应 ErrNotFound：%v", err)
	}

	if _, err := NewAccountRepo(nil); err == nil {
		t.Fatal("接受 nil db")
	}
	//nolint:staticcheck // 刻意传 nil 验证防御边界（SA1012），不是生产调用。
	if _, err := repo.List(nil); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("接受 nil ctx：%v", err)
	}
}
