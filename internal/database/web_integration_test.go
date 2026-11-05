//go:build integration

package database

// 本文件用真实 PG 测面板事件详情与按通道查询：
// 详情要拼出 send_plans/send_attempts，列表只按 channel/dir 过滤。

import (
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

// TestWebEventsByChannelAndDetail 覆盖通道/方向过滤、事件详情与回复尝试拼装。
func TestWebEventsByChannelAndDetail(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewWebRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	insert := func(id, channel, dir, text string, private bool, at time.Time) {
		t.Helper()
		if err := db.WithContext(ctx).Exec(
			`INSERT INTO events (id, channel, dir, session, from_id, from_name, text, private, reply_to, at, status, meta)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb)`,
			id, channel, dir, "sess", "778", "观众"+id, text, private, "@鱼", at, "done", `{"gift":true}`,
		).Error; err != nil {
			t.Fatalf("插入事件失败：%v", err)
		}
	}
	insert("live:1", "live", "in", "直播弹幕", false, base)
	insert("comment:1", "comment", "in", "评论一", false, base.Add(time.Minute))
	insert("comment:2", "comment", "out", "评论回复", false, base.Add(2*time.Minute))
	insert("dm:1", "dm", "in", "私信", true, base.Add(3*time.Minute))
	insert("live:2", "live", "out", "直播回复", false, base.Add(4*time.Minute))

	rows, err := repo.EventsByChannel(ctx, "comment", "", 10)
	if err != nil {
		t.Fatalf("按通道查询失败：%v", err)
	}
	if len(rows) != 2 || rows[0].ID != "comment:2" || rows[1].ID != "comment:1" {
		t.Fatalf("通道过滤/排序不符：%+v", rows)
	}
	rows, err = repo.EventsByChannel(ctx, "comment", "in", 10)
	if err != nil {
		t.Fatalf("带方向查询失败：%v", err)
	}
	if len(rows) != 1 || rows[0].ID != "comment:1" {
		t.Fatalf("方向过滤不符：%+v", rows)
	}
	if _, err := repo.EventsByChannel(ctx, "qq", "", 10); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("非法通道应 ErrInvalid：%v", err)
	}
	if _, err := repo.EventsByChannel(ctx, "comment", "side", 10); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("非法方向应 ErrInvalid：%v", err)
	}
	if _, err := repo.EventsByChannel(ctx, "comment", "", 0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("非法 limit 应 ErrInvalid：%v", err)
	}

	// 回复计划与两次尝试：一个成功、一个被拒；详情按 number 升序拼装。
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := tx.Exec(
		`INSERT INTO send_plans (event_id, actor_id, room_id, text, text_sha, state, current_attempt_id, attempt_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"live:1", "778", "3030", "在的", "sha-1", "sent", "att-1", 2,
	).Error; err != nil {
		t.Fatalf("插入回复计划失败：%v", err)
	}
	for _, a := range []struct {
		id        string
		number    int
		state     string
		ok        bool
		verified  bool
		msgID     string
		errorKind string
	}{
		{"att-1", 1, "accepted", true, true, "m-1", ""},
		{"att-2", 2, "rejected", false, false, "", "rate_limit"},
	} {
		if err := tx.Exec(
			`INSERT INTO send_attempts (id, event_id, actor_id, room_id, text_sha, number, state, ok, verified, msg_id, error_kind)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.id, "live:1", "778", "3030", "sha-1", a.number, a.state, a.ok, a.verified, a.msgID, a.errorKind,
		).Error; err != nil {
			t.Fatalf("插入投递尝试失败：%v", err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("提交回复数据失败：%v", err)
	}

	detail, err := repo.Event(ctx, "live:1")
	if err != nil {
		t.Fatalf("读取事件详情失败：%v", err)
	}
	if detail.FromID != "778" || detail.ReplyTo != "@鱼" || detail.Private ||
		detail.Text != "直播弹幕" || detail.Status != "done" || detail.CreatedAt.IsZero() {
		t.Fatalf("事件字段不符：%+v", detail)
	}
	if !strings.Contains(detail.Meta, `"gift"`) {
		t.Fatalf("meta 未保留 JSON：%q", detail.Meta)
	}
	if detail.Reply == nil {
		t.Fatal("缺少回复计划")
	}
	if detail.Reply.ActorID != "778" || detail.Reply.Text != "在的" || detail.Reply.AttemptCount != 2 {
		t.Fatalf("回复计划不符：%+v", detail.Reply)
	}
	if len(detail.Reply.Attempts) != 2 {
		t.Fatalf("投递尝试数量不符：%+v", detail.Reply.Attempts)
	}
	first, second := detail.Reply.Attempts[0], detail.Reply.Attempts[1]
	if first.Number != 1 || first.OK == nil || !*first.OK || !first.Verified || first.MsgID != "m-1" {
		t.Fatalf("首次尝试不符：%+v", first)
	}
	if second.Number != 2 || second.OK == nil || *second.OK || second.ErrorKind != "rate_limit" {
		t.Fatalf("第二次尝试不符：%+v", second)
	}

	noReply, err := repo.Event(ctx, "dm:1")
	if err != nil {
		t.Fatalf("读取私信详情失败：%v", err)
	}
	if noReply.Reply != nil {
		t.Fatalf("未回复事件不应有回复计划：%+v", noReply.Reply)
	}
	if !noReply.Private {
		t.Error("私信应标记为 private")
	}

	if _, err := repo.Event(ctx, "missing"); !errors.Is(err, store.ErrWebNotFound) {
		t.Fatalf("不存在应 ErrWebNotFound：%v", err)
	}
}
