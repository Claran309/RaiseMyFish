//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

func repositoryDB(t *testing.T) (*gorm.DB, context.Context) {
	t.Helper()
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}
func testEvent(id string, ch event.Channel, session string, private bool, at time.Time) event.Event {
	return event.Event{ID: id, Channel: ch, Session: session, Private: private, Dir: event.DirIn, Text: "正常事件", At: at, Status: event.StatusPending}
}

func TestEventRepositoryRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	var _ store.EventRepo = repo
	ev := testEvent("live:room:one", event.ChLive, "room", false, time.Now().UTC().Truncate(time.Microsecond))
	ev.Meta = []byte(`{"n":1}`)
	var wg sync.WaitGroup
	var inserted atomic.Int32
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			ok, e := repo.Insert(ctx, ev)
			if ok {
				inserted.Add(1)
			}
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if inserted.Load() != 1 {
		t.Fatalf("并发插入%d次", inserted.Load())
	}
	got, err := repo.Get(ctx, ev.ID)
	if err != nil || got.ID != ev.ID || got.Text != ev.Text || !got.At.Equal(ev.At) || got.Status != event.StatusPending {
		t.Fatalf("读取结果错误: %v", err)
	}
	changed := ev
	changed.Text = "不该覆盖"
	ok, err := repo.Insert(ctx, changed)
	if err != nil || ok {
		t.Fatal("重复插入错误")
	}
	got, _ = repo.Get(ctx, ev.ID)
	if got.Text != ev.Text {
		t.Fatal("重复覆盖原事件")
	}
	if err := repo.Transition(ctx, ev.ID, []event.Status{event.StatusPending}, event.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transition(ctx, ev.ID, []event.Status{event.StatusPending}, event.StatusQueued); !errors.Is(err, store.ErrConflict) {
		t.Fatal("陈旧迁移未失败")
	}
	if err := repo.Transition(ctx, ev.ID, []event.Status{event.StatusQueued}, event.StatusDone); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("非法迁移未失败")
	}
	if err := repo.Transition(ctx, ev.ID, []event.Status{event.StatusQueued}, event.StatusReplying); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transition(ctx, ev.ID, []event.Status{event.StatusReplying}, event.StatusQueued); err != nil {
		t.Fatal("既有恢复边失败", err)
	}
	if _, err := repo.Get(ctx, "missing' OR true --"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("查无事件错误")
	}
	if err := repo.Transition(ctx, "missing", []event.Status{event.StatusPending}, event.StatusQueued); !errors.Is(err, store.ErrConflict) {
		t.Fatal("缺行迁移错误")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.Insert(canceled, ev); !errors.Is(err, context.Canceled) {
		t.Fatal("取消未保留")
	}
	if _, err := NewEventRepo(nil); err == nil {
		t.Fatal("接受nil db")
	}
	//nolint:staticcheck // 刻意传nil验证防御边界（SA1012），不是生产调用。
	if _, err := repo.Insert(nil, ev); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("接受nil ctx")
	}
}

func TestTaskRepositoryRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, _ := NewEventRepo(db)
	repo, err := NewTaskRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	var _ store.TaskRepo = repo
	ev := testEvent("live:room:task", event.ChLive, "room", false, time.Now())
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatal(err)
	}
	task := store.Task{ID: "t1", EventID: ev.ID, Channel: ev.Channel, Session: ev.Session, Brain: "openai", Priority: 2, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	if _, err := repo.CreateForEvent(ctx, task); !errors.Is(err, store.ErrConflict) {
		t.Fatal("未queued事件不能创建任务")
	}
	if err := events.Transition(ctx, ev.ID, []event.Status{event.StatusPending}, event.StatusQueued); err != nil {
		t.Fatal(err)
	}
	wrong := task
	wrong.Session = "another"
	if _, err := repo.CreateForEvent(ctx, wrong); !errors.Is(err, store.ErrConflict) {
		t.Fatal("跨会话任务被接受")
	}
	var wg sync.WaitGroup
	var inserted atomic.Int32
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() {
			candidate := task
			candidate.ID = fmt.Sprintf("t%d", i)
			ok, e := repo.CreateForEvent(ctx, candidate)
			if ok {
				inserted.Add(1)
			}
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if inserted.Load() != 1 {
		t.Fatal("每事件必须只有一个任务")
	}
	got, err := repo.GetByEvent(ctx, ev.ID)
	if err != nil || got.EventID != task.EventID || got.Priority != 2 || !got.CreatedAt.Equal(task.CreatedAt) {
		t.Fatal("任务读取错误", err)
	}
	other := ev
	other.ID = "live:room:other"
	if _, err := events.Insert(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := events.Transition(ctx, other.ID, []event.Status{event.StatusPending}, event.StatusQueued); err != nil {
		t.Fatal(err)
	}
	clash := task
	clash.ID = got.ID
	clash.EventID = other.ID
	if _, err := repo.CreateForEvent(ctx, clash); !errors.Is(err, store.ErrConflict) {
		t.Fatal("跨事件task ID冲突未拒绝")
	}
	if _, err := repo.GetByEvent(ctx, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("失败事务留下任务")
	}
	clash.ID = "unique"
	if ok, err := repo.CreateForEvent(ctx, clash); err != nil || !ok {
		t.Fatal("冲突后不能恢复", err)
	}
	missing := task
	missing.ID = "missing"
	missing.EventID = "absent"
	if _, err := repo.CreateForEvent(ctx, missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("不存在事件错误")
	}
	if _, err := NewTaskRepo(nil); err == nil {
		t.Fatal("接受nil db")
	}
}

func TestMemoryRepositorySQLIsolation(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, _ := NewEventRepo(db)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	var _ store.MemoryRepo = repo
	now := time.Now().UTC().Truncate(time.Microsecond)
	fixtures := []event.Event{
		testEvent("live:room:a", event.ChLive, "room", false, now), testEvent("live:room:b", event.ChLive, "room", false, now),
		testEvent("comment:video:a", event.ChComment, "video", false, now), testEvent("dm:a:one", event.ChDM, "a", true, now),
		testEvent("dm:b:one", event.ChDM, "b", true, now), testEvent("qq:bot:secret", event.ChQQ, "a", true, now),
	}
	for _, ev := range fixtures {
		if _, err := events.Insert(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	// 模拟旧表绕过写校验的错误降密；查询本身仍须挡住。
	if err := db.Exec("INSERT INTO events(id,channel,dir,session,text,private,at) VALUES ('dm:bad:one','dm','in','bad','不能外泄',false,?)", now).Error; err != nil {
		t.Fatal(err)
	}
	records := []store.MemoryRecord{
		{Record: memory.Record{ID: "global", Summary: "公开", Importance: .8, Confidence: .8, ValidFrom: now.Add(-time.Hour)}, Source: "owner_direct", Kind: "fact", Title: "公开"},
		{Record: memory.Record{ID: "private-a", Channel: event.ChDM, Session: "a", Private: true, Summary: "甲私密", Importance: 1, Confidence: .8, ValidFrom: now.Add(-time.Hour)}, Source: "owner_direct", Kind: "fact", Title: "甲", Body: "不传给Brain"},
		{Record: memory.Record{ID: "private-b", Channel: event.ChDM, Session: "b", Private: true, Summary: "乙私密", Importance: 1, Confidence: .8, ValidFrom: now.Add(-time.Hour)}, Source: "owner_direct", Kind: "fact", Title: "乙"},
	}
	records[1].EventIDs = []string{"dm:a:one"}
	for _, r := range records {
		if ok, err := repo.InsertRecord(ctx, r); err != nil || !ok {
			t.Fatal("记忆写入失败", err)
		}
	}
	if ok, err := repo.InsertRecord(ctx, records[1]); err != nil || ok {
		t.Fatal("记忆幂等错误", err)
	}
	pub := memory.Scope{Channel: event.ChLive, Session: "room"}
	priv := memory.Scope{Channel: event.ChDM, Session: "a", Private: true}
	recent, err := repo.Recent(ctx, pub, 1)
	if err != nil || len(recent) != 1 || recent[0].EventID != "live:room:b" {
		t.Fatal("最新滑窗错误", err)
	}
	recent, err = repo.Recent(ctx, pub, 10)
	if err != nil || len(recent) != 2 || recent[0].EventID != "live:room:a" {
		t.Fatal("稳定升序错误", err)
	}
	privateRecent, err := repo.Recent(ctx, priv, 10)
	if err != nil || len(privateRecent) != 1 || privateRecent[0].EventID != "dm:a:one" {
		t.Fatal("私密会话隔离错误", err)
	}
	cross, err := repo.CrossChannel(ctx, priv, 10)
	if err != nil || len(cross) != 3 {
		t.Fatal("跨通道私密泄漏/误杀", err)
	}
	for _, m := range cross {
		if m.Private || m.Channel == event.ChDM {
			t.Fatal("CrossChannel越权")
		}
	}
	rows, err := repo.Records(ctx, pub, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != "global" {
		t.Fatal("公开读取私密记录", err)
	}
	rows, err = repo.Records(ctx, priv, 10)
	if err != nil || len(rows) != 2 || rows[0].ID != "private-a" || !reflect.DeepEqual(rows[0].EventIDs, []string{"dm:a:one"}) {
		t.Fatal("私密摘要/来源读取错误", err)
	}
	for _, column := range []string{"archived", "valid_to"} {
		if column == "archived" {
			err = db.Exec("UPDATE memory_records SET archived=true WHERE id='global'").Error
		} else {
			err = db.Exec("UPDATE memory_records SET archived=false,valid_to=? WHERE id='global'", now).Error
		}
		if err != nil {
			t.Fatal(err)
		}
		rows, err = repo.Records(ctx, pub, 10)
		if err != nil || len(rows) != 0 {
			t.Fatal("失效记录被返回", err)
		}
	}
	injection := pub
	injection.Session = "room' OR true --"
	rows2, err := repo.Recent(ctx, injection, 10)
	if err != nil || len(rows2) != 0 {
		t.Fatal("参数化隔离错误", err)
	}
	for _, limit := range []int{0, -1, 101} {
		if _, err := repo.Recent(ctx, pub, limit); !errors.Is(err, store.ErrInvalid) {
			t.Fatal("未限制查询规模")
		}
	}
	invalid := memory.Scope{Channel: event.ChDM, Session: "a"}
	if _, err := repo.Records(ctx, invalid, 10); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("无效scope未拒绝")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.Records(canceled, pub, 10); !errors.Is(err, context.Canceled) {
		t.Fatal("读取取消未透传")
	}
	badSource := records[0]
	badSource.ID = "bad-source"
	badSource.Source = "sensitive-user-input-xxx"
	if _, err := repo.InsertRecord(ctx, badSource); err == nil || strings.Contains(err.Error(), badSource.Source) {
		t.Fatal("写失败未脱敏")
	}
	if _, err := NewMemoryRepo(nil); err == nil {
		t.Fatal("接受nil db")
	}
}

func TestEventTransitionConcurrentCAS(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, _ := NewEventRepo(db)
	ev := testEvent("live:room:cas", event.ChLive, "room", false, time.Now())
	if _, err := repo.Insert(ctx, ev); err != nil {
		t.Fatal(err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Go(func() {
			to := event.StatusQueued
			if i%2 == 0 {
				to = event.StatusSkipped
			}
			err := repo.Transition(ctx, ev.ID, []event.Status{event.StatusPending}, to)
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, store.ErrConflict) {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if won.Load() != 1 {
		t.Fatal("CAS只能一人成功")
	}
}

func TestMemoryQueriesPrivateQQAndOrdering(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, _ := NewEventRepo(db)
	repo, _ := NewMemoryRepo(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, ev := range []event.Event{
		testEvent("qq:bot:public", event.ChQQ, "same", false, now.Add(-time.Second)),
		testEvent("qq:bot:private", event.ChQQ, "same", true, now),
		testEvent("qq:bot:other", event.ChQQ, "other", true, now),
		testEvent("live:room:public", event.ChLive, "room", false, now),
	} {
		if _, err := events.Insert(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	pub := memory.Scope{Channel: event.ChQQ, Session: "same"}
	priv := pub
	priv.Private = true
	for _, test := range []struct {
		scope memory.Scope
		want  int
	}{{pub, 1}, {priv, 2}} {
		rows, err := repo.Recent(ctx, test.scope, 10)
		if err != nil || len(rows) != test.want {
			t.Fatal("QQ可见范围错误", err)
		}
		if err := (memory.Context{Scope: test.scope, Recent: rows}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []memory.Scope{pub, priv} {
		rows, err := repo.CrossChannel(ctx, scope, 10)
		if err != nil || len(rows) != 1 || rows[0].Channel != event.ChLive {
			t.Fatal("同通道/私密误入跨通道", err)
		}
	}
	for _, id := range []string{"b", "a", "future"} {
		validFrom := now.Add(-time.Hour)
		if id == "future" {
			validFrom = now.Add(time.Hour)
		}
		record := store.MemoryRecord{Record: memory.Record{ID: id, Summary: "公开", ValidFrom: validFrom, Importance: .5, Confidence: .8}, Source: "owner_direct", Title: "标题", Kind: "fact"}
		if _, err := repo.InsertRecord(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := repo.Records(ctx, pub, 10)
	if err != nil || len(rows) != 2 || rows[0].ID != "a" || rows[1].ID != "b" {
		t.Fatal("未来记录/稳定排序错误", err)
	}
	// 旧表的DM公开记录不应借公开标志泄漏到任意Scope。
	if err := db.Exec("INSERT INTO memory_records(id,source,kind,title,summary,private,channel,session) VALUES ('corrupt','owner_direct','fact','秘密','不能召回',false,'dm','a')").Error; err != nil {
		t.Fatal(err)
	}
	rows, err = repo.Records(ctx, pub, 10)
	if err != nil || len(rows) != 2 {
		t.Fatal("旧记录被错误降密", err)
	}
}

func TestRepositoryInvalidAndCanceledPaths(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, _ := NewEventRepo(db)
	tasks, _ := NewTaskRepo(db)
	memories, _ := NewMemoryRepo(db)
	ev := testEvent("live:room:errors", event.ChLive, "room", false, time.Now())
	if _, err := events.Insert(ctx, event.Event{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	//nolint:staticcheck // 刻意传nil验证防御边界（SA1012），不是生产调用。
	if _, err := events.Get(nil, "x"); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	//nolint:staticcheck // 刻意传nil验证防御边界（SA1012），不是生产调用。
	if err := events.Transition(nil, "x", nil, event.StatusQueued); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	//nolint:staticcheck // 刻意传nil验证防御边界（SA1012），不是生产调用。
	if _, err := tasks.CreateForEvent(nil, store.Task{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := tasks.CreateForEvent(ctx, store.Task{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	//nolint:staticcheck // 刻意传nil验证防御边界（SA1012），不是生产调用。
	if _, err := tasks.GetByEvent(nil, "x"); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	//nolint:staticcheck // 刻意传nil验证防御边界（SA1012），不是生产调用。
	if _, err := memories.InsertRecord(nil, store.MemoryRecord{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := memories.InsertRecord(ctx, store.MemoryRecord{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	scope := memory.Scope{Channel: event.ChLive, Session: "room"}
	//nolint:staticcheck // 刻意传nil验证防御边界（SA1012），不是生产调用。
	if _, err := memories.CrossChannel(nil, scope, 1); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	//nolint:staticcheck // 刻意传nil验证防御边界（SA1012），不是生产调用。
	if _, err := memories.Records(nil, scope, 1); !errors.Is(err, store.ErrInvalid) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := events.Get(canceled, ev.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := events.Transition(canceled, ev.ID, []event.Status{event.StatusPending}, event.StatusQueued); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	task := store.Task{ID: "task", EventID: ev.ID, Channel: ev.Channel, Session: ev.Session, Brain: "openai", Priority: 2, CreatedAt: time.Now()}
	if _, err := tasks.CreateForEvent(canceled, task); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := tasks.GetByEvent(canceled, ev.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := memories.Recent(canceled, scope, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := memories.CrossChannel(canceled, scope, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	record := store.MemoryRecord{Record: memory.Record{ID: "cancel", Summary: "摘要", Importance: .5, Confidence: .8, ValidFrom: time.Now()}, Source: "owner_direct", Kind: "fact", Title: "标题"}
	if _, err := memories.InsertRecord(canceled, record); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRepositoryClosedConnectionErrorsAreSafe(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, _ := NewEventRepo(db)
	tasks, _ := NewTaskRepo(db)
	memories, _ := NewMemoryRepo(db)
	ev := testEvent("live:room:secret-canary", event.ChLive, "room", false, time.Now())
	task := store.Task{ID: "task", EventID: ev.ID, Channel: ev.Channel, Session: ev.Session, Brain: "openai", Priority: 2, CreatedAt: time.Now()}
	record := store.MemoryRecord{Record: memory.Record{ID: "secret-canary", Summary: "摘要", Importance: .5, Confidence: .8, ValidFrom: time.Now()}, Source: "owner_direct", Kind: "fact", Title: "标题"}
	scope := memory.Scope{Channel: event.ChLive, Session: "secret-canary"}
	if err := Close(db); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{"event-insert", func() error { _, err := events.Insert(ctx, ev); return err }},
		{"event-get", func() error { _, err := events.Get(ctx, ev.ID); return err }},
		{"event-transition", func() error {
			return events.Transition(ctx, ev.ID, []event.Status{event.StatusPending}, event.StatusQueued)
		}},
		{"task-create", func() error { _, err := tasks.CreateForEvent(ctx, task); return err }},
		{"task-get", func() error { _, err := tasks.GetByEvent(ctx, ev.ID); return err }},
		{"memory-insert", func() error { _, err := memories.InsertRecord(ctx, record); return err }},
		{"memory-recent", func() error { _, err := memories.Recent(ctx, scope, 10); return err }},
		{"memory-cross", func() error { _, err := memories.CrossChannel(ctx, scope, 10); return err }},
		{"memory-records", func() error { _, err := memories.Records(ctx, scope, 10); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if err == nil || errors.Unwrap(err) == nil {
				t.Fatal("数据库失败未保留错误链")
			}
			if strings.Contains(err.Error(), "secret-canary") || strings.Contains(err.Error(), "closed") {
				t.Fatal("错误展示泄露了入参或底层错误")
			}
		})
	}
}
