//go:build integration

package database

import (
	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/collector/live"
	"RaiseMyFish/internal/event"
	"context"
	"go.uber.org/zap"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type observedPublisher struct {
	bus       event.Bus
	calls     atomic.Int32
	published chan struct{}
}

func (p *observedPublisher) Publish(ctx context.Context, e event.Event) (bool, error) {
	ok, err := p.bus.Publish(ctx, e)
	if err == nil && p.calls.Add(1) == 2 {
		close(p.published)
	}
	return ok, err
}
func TestCollectorNodeToRealPostgresBus(t *testing.T) {
	db, ctx := repositoryDB(t)
	bus := makeBus(t, db)
	o := live.DefaultOptions()
	o.RoomID = "1"
	o.SelfID = "8"
	o.Cookie = "SESSDATA=fixture-canary"
	o.ScriptPath = filepath.Join("..", "collector", "live", "testdata", "stream.mjs")
	c, err := live.New(o, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	publisher := &observedPublisher{bus: bus, published: make(chan struct{})}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- collector.RunToBus(runCtx, c, publisher, time.Second, zap.NewNop()) }()
	select {
	case <-publisher.published:
	case err := <-finished:
		t.Fatal("采集提前退出", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Node到PG未完成")
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Node未回收")
	}
	var count int64
	if err := db.Table("events").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("重复或自身事件进入PG", count, err)
	}
	repo, _ := NewEventRepo(db)
	ev, err := repo.Get(ctx, "live:99:9007199254740993123")
	if err != nil || ev.Session != "99" || ev.FromID != "7" || ev.Private || ev.Status != event.StatusPending {
		t.Fatal("稳定ID/房间/事件映射错误", err)
	}
	if err := db.Table("llm_usage").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("采集路径不应调用模型", err)
	}
	if c.Health().OK {
		t.Fatal("停止后仍显示连接正常")
	}
}
