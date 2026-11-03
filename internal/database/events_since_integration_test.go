//go:build integration

package database

import (
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

// TestPublicSinceRealPostgres 验证时间窗口、通道与公开范围都在 SQL 层生效。
func TestPublicSinceRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	seeds := []event.Event{
		inEvent("live:room:old", event.ChLive, "room", false, base.Add(-2*time.Hour), "窗口外公开"),
		inEvent("live:room:new", event.ChLive, "room", false, base.Add(-time.Minute), "窗口内公开"),
		inEvent("comment:c1:new", event.ChComment, "c1", false, base.Add(-time.Minute), "其他通道"),
		inEvent("qq:group:new", event.ChQQ, "group-1", false, base.Add(-time.Minute), "QQ群公开"),
		inEvent("qq:u1:private", event.ChQQ, "u1", true, base.Add(-time.Minute), "QQ私密"),
	}
	for _, ev := range seeds {
		if _, err := repo.Insert(ctx, ev); err != nil {
			t.Fatalf("插入事件 %s 失败：%v", ev.ID, err)
		}
	}

	got, err := repo.PublicSince(ctx, event.ChLive, base.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "live:room:new" {
		t.Fatalf("窗口查询结果不符：%+v", got)
	}
	if got[0].Private || got[0].Dir != event.DirIn {
		t.Fatalf("返回了非公开入向事件：%+v", got[0])
	}
	qq, err := repo.PublicSince(ctx, event.ChQQ, base.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(qq) != 1 || qq[0].ID != "qq:group:new" || qq[0].Private {
		t.Fatalf("私密事件未被排除：%+v", qq)
	}

	for name, run := range map[string]func() ([]event.Event, error){
		"空ctx": func() ([]event.Event, error) { return repo.PublicSince(nil, event.ChLive, base, 10) }, //nolint:staticcheck // 专门验证nil ctx的错误路径。
		"坏通道":  func() ([]event.Event, error) { return repo.PublicSince(ctx, "bad", base, 10) },
		"零时间":  func() ([]event.Event, error) { return repo.PublicSince(ctx, event.ChLive, time.Time{}, 10) },
		"零上限":  func() ([]event.Event, error) { return repo.PublicSince(ctx, event.ChLive, base, 0) },
		"超上限":  func() ([]event.Event, error) { return repo.PublicSince(ctx, event.ChLive, base, maxSinceEvents+1) },
	} {
		if _, err := run(); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("%s 未返回 ErrInvalid：%v", name, err)
		}
	}

	limited, err := repo.PublicSince(ctx, event.ChLive, base.Add(-time.Hour), 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limit 未生效：%v %+v", err, limited)
	}
}
