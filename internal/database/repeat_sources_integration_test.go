//go:build integration

package database

// 重复度触发的三个新查询：跨通道路由、活跃昵称、按标题查重与降权。

import (
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// TestPublicIncomingSinceRealPostgres 跨通道返回公开入向事件，DM/私密/出向被排除。
func TestPublicIncomingSinceRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	seeds := []event.Event{
		inEvent("live:room:old", event.ChLive, "room", false, base.Add(-2*time.Hour), "窗口外"),
		inEvent("live:room:new", event.ChLive, "room", false, base.Add(-2*time.Minute), "窗口内直播"),
		inEvent("comment:c1:new", event.ChComment, "c1", false, base.Add(-time.Minute), "窗口内评论"),
		inEvent("qq:group:new", event.ChQQ, "group-1", false, base.Add(-time.Minute), "QQ公开"),
		inEvent("qq:u1:private", event.ChQQ, "u1", true, base.Add(-time.Minute), "QQ私密"),
		inEvent("dm:u1:new", event.ChDM, "dm-a", true, base.Add(-time.Minute), "私信"),
	}
	out := inEvent("live:room:out", event.ChLive, "room", false, base.Add(-time.Minute), "鱼的回复")
	out.Dir = event.DirOut
	seeds = append(seeds, out)
	for _, ev := range seeds {
		if _, err := repo.Insert(ctx, ev); err != nil {
			t.Fatalf("插入事件 %s 失败：%v", ev.ID, err)
		}
	}

	got, err := repo.PublicIncomingSince(ctx, base.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	/* 期望三条公开入向、按时间倒序（同时间按 ID 倒序）：comment/c1 与 qq/group 同一分钟。 */
	if len(got) != 3 {
		t.Fatalf("跨通道窗口结果数 = %d，期望 3：%+v", len(got), got)
	}
	ids := map[string]bool{}
	for _, ev := range got {
		if ev.Private || ev.Dir != event.DirIn || ev.Channel == event.ChDM {
			t.Fatalf("返回了非公开事件：%+v", ev)
		}
		ids[ev.ID] = true
	}
	for _, want := range []string{"live:room:new", "comment:c1:new", "qq:group:new"} {
		if !ids[want] {
			t.Errorf("缺少事件 %s：%+v", want, got)
		}
	}

	for name, run := range map[string]func() ([]event.Event, error){
		"空ctx": func() ([]event.Event, error) { return repo.PublicIncomingSince(nil, base, 10) }, //nolint:staticcheck // 专门验证nil ctx的错误路径。
		"零时间":  func() ([]event.Event, error) { return repo.PublicIncomingSince(ctx, time.Time{}, 10) },
		"零上限":  func() ([]event.Event, error) { return repo.PublicIncomingSince(ctx, base, 0) },
		"超上限":  func() ([]event.Event, error) { return repo.PublicIncomingSince(ctx, base, maxSinceEvents+1) },
	} {
		if _, err := run(); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("%s 未返回 ErrInvalid：%v", name, err)
		}
	}
}

// TestRepeatViewerNamesRealPostgres 只读窗口内的活跃昵称，空昵称与过期档案被排除。
func TestRepeatViewerNamesRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	profiles := []store.ViewerProfile{
		{UID: "7001", Name: "X奇迹与你X", Hits: 20, LastSeen: base.Add(-time.Hour)},
		{UID: "7002", Name: "终南仙主", Hits: 10, LastSeen: base.Add(-2 * time.Hour)},
		{UID: "7003", Name: "过期观众", Hits: 99, LastSeen: base.Add(-30 * 24 * time.Hour)},
	}
	for _, p := range profiles {
		if err := repo.UpsertProfile(ctx, p); err != nil {
			t.Fatalf("写入档案 %s 失败：%v", p.UID, err)
		}
	}
	// 昵称只有空白的档案不参与统计。
	if err := repo.UpsertProfile(ctx, store.ViewerProfile{UID: "7004", Name: "  ", Hits: 50, LastSeen: base}); err != nil {
		t.Fatalf("写入空昵称档案失败：%v", err)
	}

	got, err := repo.RepeatViewerNames(ctx, base.Add(-7*24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].UID != "7001" || got[1].UID != "7002" {
		t.Fatalf("活跃昵称结果不符：%+v", got)
	}
	if got[0].Name != "X奇迹与你X" {
		t.Errorf("昵称映射不符：%+v", got[0])
	}

	for name, run := range map[string]func() ([]store.ViewerName, error){
		"空ctx": func() ([]store.ViewerName, error) { return repo.RepeatViewerNames(nil, base, 10) }, //nolint:staticcheck // 专门验证nil ctx的错误路径。
		"零时间":  func() ([]store.ViewerName, error) { return repo.RepeatViewerNames(ctx, time.Time{}, 10) },
		"零上限":  func() ([]store.ViewerName, error) { return repo.RepeatViewerNames(ctx, base, 0) },
		"超上限":  func() ([]store.ViewerName, error) { return repo.RepeatViewerNames(ctx, base, 501) },
	} {
		if _, err := run(); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("%s 未返回 ErrInvalid：%v", name, err)
		}
	}
}

// TestRepeatFactByTitleAndDownweight 公开事实按标题查重、只看公开行、降权不反弹。
func TestRepeatFactByTitleAndDownweight(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	title := "称呼：奇迹鱼 = X奇迹与你X"
	public := store.MemoryRecord{
		Record: memory.Record{ID: "fact:public:repeat:1", Summary: "弹幕里「奇迹鱼」指的是 X奇迹与你X（7 天内提及 14 次）",
			Importance: 0.5, Confidence: 0.8, EventIDs: []string{"live:1:1"}, ValidFrom: now.Add(-time.Hour)},
		Source: "repeat", Kind: "fact", Title: title, Subject: memory.SoulSubjectViewer, DerivedBy: "rule:repeat/v1",
	}
	if ok, err := repo.InsertRecord(ctx, public); err != nil || !ok {
		t.Fatalf("写入公开事实失败：%v", err)
	}
	private := public
	private.Record.ID = "fact:dm:repeat:1"
	private.Record.Channel, private.Record.Session, private.Record.Private = event.ChDM, "dm-a", true
	private.Record.ValidFrom = now
	if ok, err := repo.InsertRecord(ctx, private); err != nil || !ok {
		t.Fatalf("写入私密同名事实失败：%v", err)
	}

	state, found, err := repo.RepeatFactByTitle(ctx, title)
	if err != nil || !found {
		t.Fatalf("查重失败：found=%v err=%v", found, err)
	}
	if state.ID != public.ID || state.Archived || !state.ValidFrom.Equal(public.ValidFrom) {
		t.Fatalf("应命中公开行：%+v", state)
	}

	if ok, err := repo.DownweightFact(ctx, state.ID, 0.2, 0.5); err != nil || !ok {
		t.Fatalf("降权失败：ok=%v err=%v", ok, err)
	}
	state, _, err = repo.RepeatFactByTitle(ctx, title)
	if err != nil {
		t.Fatal(err)
	}
	if state.Importance != 0.2 || state.Confidence != 0.5 {
		t.Fatalf("降权未生效：%+v", state)
	}
	// LEAST 语义：更高的目标值不能把权重抬回去。
	if ok, err := repo.DownweightFact(ctx, state.ID, 0.9, 0.9); err != nil || !ok {
		t.Fatalf("重复降权失败：ok=%v err=%v", ok, err)
	}
	state, _, err = repo.RepeatFactByTitle(ctx, title)
	if err != nil {
		t.Fatal(err)
	}
	if state.Importance != 0.2 || state.Confidence != 0.5 {
		t.Fatalf("降权不应反弹：%+v", state)
	}

	// 归档行不能被降权；查重仍能读到归档状态（用于阻止重复提升）。
	if err := repo.db.WithContext(ctx).Exec("UPDATE memory_records SET archived=true WHERE id=?", public.ID).Error; err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.DownweightFact(ctx, public.ID, 0.1, 0.1); err != nil || ok {
		t.Fatalf("归档行不应降权：ok=%v err=%v", ok, err)
	}
	state, found, err = repo.RepeatFactByTitle(ctx, title)
	if err != nil || !found || !state.Archived {
		t.Fatalf("归档状态未返回：%+v found=%v err=%v", state, found, err)
	}

	if _, _, err := repo.RepeatFactByTitle(ctx, ""); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("空标题未返回 ErrInvalid：%v", err)
	}
	if _, err := repo.DownweightFact(ctx, public.ID, 1.5, 0.5); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("非法权重未返回 ErrInvalid：%v", err)
	}
}
