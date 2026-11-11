//go:build integration

package database

// 本文件用真实 PG 测看板注入仓储：有效期过滤、CRUD、内容校验与不存在错误。

import (
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

func TestInjectionRepositoryRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewWebRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	perm, err := repo.CreateInjection(ctx, " 永久设定 ", nil)
	if err != nil {
		t.Fatalf("写永久注入失败：%v", err)
	}
	if perm.Content != "永久设定" || perm.ExpiresAt != nil || perm.ID < 1 {
		t.Fatalf("永久注入返回错误：%+v", perm)
	}
	expires := now.Add(time.Hour)
	temp, err := repo.CreateInjection(ctx, "临时设定", &expires)
	if err != nil {
		t.Fatalf("写临时注入失败：%v", err)
	}
	past := now.Add(-time.Hour)
	if _, err := repo.CreateInjection(ctx, "过期设定", &past); err != nil {
		t.Fatalf("写过期注入失败：%v", err)
	}

	rows, err := repo.ActiveInjections(ctx, now)
	if err != nil {
		t.Fatalf("读有效注入失败：%v", err)
	}
	if len(rows) != 2 || rows[0].ID != temp.ID || rows[1].ID != perm.ID {
		t.Fatalf("有效注入应只含未过期两条且新的在前：%+v", rows)
	}

	updated, err := repo.UpdateInjection(ctx, temp.ID, "改后设定", nil)
	if err != nil {
		t.Fatalf("更新注入失败：%v", err)
	}
	if updated.Content != "改后设定" || updated.ExpiresAt != nil {
		t.Fatalf("更新结果错误：%+v", updated)
	}

	if err := repo.DeleteInjection(ctx, perm.ID); err != nil {
		t.Fatalf("删除注入失败：%v", err)
	}
	if err := repo.DeleteInjection(ctx, perm.ID); !errors.Is(err, store.ErrWebNotFound) {
		t.Fatalf("重复删除应返回 not found：%v", err)
	}

	if _, err := repo.CreateInjection(ctx, "   ", nil); !errors.Is(err, store.ErrInjectionInvalid) {
		t.Fatalf("空内容应返回 invalid：%v", err)
	}
	if _, err := repo.CreateInjection(ctx, strings.Repeat("设", store.MaxInjectionRunes+1), nil); !errors.Is(err, store.ErrInjectionInvalid) {
		t.Fatalf("超长内容应返回 invalid：%v", err)
	}
	if _, err := repo.UpdateInjection(ctx, 999999, "不存在", nil); !errors.Is(err, store.ErrWebNotFound) {
		t.Fatalf("更新不存在应返回 not found：%v", err)
	}

	left, err := repo.ActiveInjections(ctx, now)
	if err != nil || len(left) != 1 || left[0].ID != temp.ID {
		t.Fatalf("删除后应只剩更新过的一条：%+v %v", left, err)
	}
}
