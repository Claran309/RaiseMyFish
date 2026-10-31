//go:build integration

package database

import (
	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/store"
	"context"
	"go.uber.org/zap"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUsageRepoRealPostgres(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo, err := NewUsageRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	unknown := store.Usage{At: time.Now(), Channel: event.ChLive, Model: "fake:model", Milliseconds: 10, OK: false}
	if err := repo.RecordUsage(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	in, out := 10, 3
	cost := .000123
	known := unknown
	known.InputTokens = &in
	known.OutputTokens = &out
	known.CostUSD = &cost
	known.OK = true
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- repo.RecordUsage(ctx, known) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var total, nulls, success int64
	if err := db.Table("llm_usage").Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("llm_usage").Where("in_tokens IS NULL AND out_tokens IS NULL AND cost_usd IS NULL AND ok=false").Count(&nulls).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("llm_usage").Where("in_tokens=? AND out_tokens=? AND cost_usd=? AND ok=true", in, out, cost).Count(&success).Error; err != nil {
		t.Fatal(err)
	}
	if total != 5 || nulls != 1 || success != 4 {
		t.Fatalf("账本结果错误：%d/%d/%d", total, nulls, success)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := repo.RecordUsage(canceled, known); err == nil {
		t.Fatal("取消写入未返回错误")
	}
	if _, err := NewUsageRepo(nil); err == nil {
		t.Fatal("接受nil数据库")
	}
	invalid := known
	invalid.Model = ""
	if err := repo.RecordUsage(ctx, invalid); err == nil {
		t.Fatal("接受无效用量")
	}
}

func TestBrainHTTPAttemptsReachRealUsageLedger(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo, err := NewUsageRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, "untrusted-error-body")
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"鱼鱼鱼鱼鱼鱼鱼鱼鱼鱼"}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`)
	}))
	defer server.Close()
	p, err := provider.NewOpenAI("fake", server.URL, "not-real-key", server.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	o := brain.DefaultOptions()
	o.Model = "model"
	o.Backoff = time.Millisecond
	b, err := brain.New(p, repo, o, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	pack, err := persona.Load(ctx, "../../personas/dafeiyu")
	if err != nil {
		t.Fatal(err)
	}
	scope := memory.Scope{Channel: event.ChLive, Session: "room"}
	task := brain.Task{ID: "http-ledger", EventID: "live:room:integration", Channel: event.ChLive, Session: "room"}
	in := brain.ContextInput{Event: event.Event{ID: task.EventID, Channel: event.ChLive, Session: "room", Dir: event.DirIn, Text: "你好", At: time.Now()}, Persona: pack, Memory: memory.Context{Scope: scope}, Shared: memory.SharedState{Scope: scope}}
	reply, err := b.Reply(ctx, task, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Text, "鱼") || calls.Load() != 2 {
		t.Fatal("HTTP重试链路失败")
	}
	var rows []struct {
		Model  string
		OK     bool
		Input  *int     `gorm:"column:in_tokens"`
		Output *int     `gorm:"column:out_tokens"`
		Cost   *float64 `gorm:"column:cost_usd"`
	}
	if err := db.Table("llm_usage").Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].OK || !rows[1].OK || rows[0].Input != nil || rows[1].Input == nil || *rows[1].Input != 100 || rows[0].Cost != nil || rows[1].Cost != nil || rows[1].Model != "fake:model" {
		t.Fatalf("实际HTTP尝试的PG账本错误：%+v", rows)
	}
}
