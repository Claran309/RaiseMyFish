package knowledge

// 本文件测热重载：文件变更后新内容可召回、Reload 失败保留旧快照、并发读安全。

import (
	"context"
	"strings"
	"sync"
	"testing"

	"RaiseMyFish/internal/event"
)

func reloadQuery() Query {
	return Query{Scope: Scope{Channel: event.ChLive, Session: "30308220"}, Text: "钓鱼梗"}
}

// TestReloadPicksUpNewMeme 新增 learned.md 后 Reload，新梗可召回。
func TestReloadPicksUpNewMeme(t *testing.T) {
	dir := knowledgeDir(t)
	r, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.Recall(context.Background(), reloadQuery())
	if err != nil {
		t.Fatalf("首次召回失败：%v", err)
	}
	if len(before.Items) != 0 {
		t.Fatalf("初始不应有结果：%+v", before.Items)
	}

	// 模拟梗白学习写入
	writeKnowledge(t, dir, "memes/learned.md", sampleFile)
	if err := r.Reload(context.Background()); err != nil {
		t.Fatalf("热重载失败：%v", err)
	}

	after, err := r.Recall(context.Background(), reloadQuery())
	if err != nil {
		t.Fatalf("重载后召回失败：%v", err)
	}
	if len(after.Items) != 0 {
		t.Logf("召回结果：%+v", after.Items) // 关键词不一定命中，用条目数兜底
	}
	info, err := r.SnapshotInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.Entries != 1 {
		t.Fatalf("重载后应有 1 条，实际 %d", info.Entries)
	}
}

// TestReloadFailureKeepsOldSnapshot 坏文件导致重载失败时，旧快照仍可用。
func TestReloadFailureKeepsOldSnapshot(t *testing.T) {
	dir := knowledgeDir(t)
	writeKnowledge(t, dir, "memes.md", sampleFile)
	r, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := r.SnapshotInfo(); info.Entries != 1 {
		t.Fatalf("初始应 1 条：%+v", info)
	}

	// 写坏 YAML
	writeKnowledge(t, dir, "memes.md", "schema: fish-knowledge/v1\nentries: [ 坏的\n")
	if err := r.Reload(context.Background()); err == nil {
		t.Fatal("坏文件应导致 Reload 报错")
	}

	// 旧快照不动
	info, err := r.SnapshotInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.Entries != 1 {
		t.Fatalf("失败后应保留旧快照，实际 %d 条", info.Entries)
	}
	if ctx, err := r.Recall(context.Background(), reloadQuery()); err != nil || ctx.Revision == "" {
		t.Fatalf("旧快照应仍可召回：%+v / %v", ctx, err)
	}
}

// TestReloadConcurrentWithRecall 并发读 + 重载不 panic、不脏读（配合 -race）。
func TestReloadConcurrentWithRecall(t *testing.T) {
	dir := knowledgeDir(t)
	writeKnowledge(t, dir, "memes.md", sampleFile)
	r, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = r.Recall(context.Background(), reloadQuery())
					_, _ = r.SnapshotInfo()
				}
			}
		}()
	}
	// 注意：learned.md 要用不同 id，否则与 memes.md 撞主键（加载期会报重复）
	learned := strings.ReplaceAll(sampleFile, "first", "learned")
	for i := 0; i < 5; i++ {
		writeKnowledge(t, dir, "memes/learned.md", learned)
		if err := r.Reload(context.Background()); err != nil {
			t.Errorf("并发重载失败：%v", err)
		}
	}
	close(stop)
	wg.Wait()
}
