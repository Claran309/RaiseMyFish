package knowledge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"RaiseMyFish/internal/event"
	"go.yaml.in/yaml/v3"
)

type cancelAfterChecks struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelAfterChecks) Err() error {
	c.checks++
	if c.checks == 10 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestRecallCancellationWithinSingleEntry(t *testing.T) {
	d := testEntry("long", "不会命中的标题")
	for i := range 1000 {
		d.Keywords = append(d.Keywords, fmt.Sprintf("key-%04d", i))
	}
	r := loadEntries(t, d)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelAfterChecks{Context: base, cancel: cancel}
	_, err := r.Recall(ctx, Query{Scope: publicScope(), Text: strings.Repeat("z", 1<<16)})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("单条长匹配期间没有响应取消")
	}
}

func testEntry(id, title string) diskEntry {
	tags, priority := []string{}, 50
	return diskEntry{ID: id, Title: title, Scenes: []string{"live", "comment", "dm", "qq_private", "qq_group"}, Tags: &tags, Priority: &priority, Visibility: "public", Source: "test:" + id, Usage: "原始接法"}
}

func loadEntries(t *testing.T, entries ...diskEntry) *FileReader {
	t.Helper()
	dir := knowledgeDir(t)
	data, err := yaml.Marshal(diskFile{Schema: "fish-knowledge/v1", Entries: &entries})
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledge(t, dir, "memes.md", "---\n"+string(data)+"---\n")
	r, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func recallIDs(t *testing.T, reader *FileReader, scope Scope, text string) []string {
	t.Helper()
	c, err := reader.Recall(context.Background(), Query{Scope: scope, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if c.Scope != scope || c.Validate() != nil {
		t.Fatal("返回Scope/Revision/权限无效")
	}
	var ids []string
	for _, item := range c.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestRecallRankTopThree(t *testing.T) {
	a, b, c, d := testEntry("title", "甲乙丙"), testEntry("keyword", "别的标题"), testEntry("tag", "第三标题"), testEntry("unrelated", "不相关")
	low, high := 1, 100
	a.Priority = &low
	b.Priority = &high
	b.Keywords = []string{"甲乙丙"}
	tags := []string{"甲乙丙"}
	c.Tags = &tags
	r := loadEntries(t, d, c, b, a)
	if got := recallIDs(t, r, publicScope(), "来个甲乙丙"); !reflect.DeepEqual(got, []string{"title", "keyword", "tag"}) {
		t.Fatal(got)
	}
	a, b, c, d = testEntry("d", "同词"), testEntry("b", "同词"), testEntry("a", "同词"), testEntry("c", "同词")
	r = loadEntries(t, a, b, c, d)
	for range 5 {
		if got := recallIDs(t, r, publicScope(), "同词同词"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
			t.Fatal(got)
		}
	}
}

func TestRecallMatchStrengthAndBoundaries(t *testing.T) {
	long, short, multi := testEntry("long", "标题A"), testEntry("short", "标题B"), testEntry("multi", "标题C")
	long.Keywords = []string{"甲乙丙"}
	short.Keywords = []string{"甲乙", "甲乙"}
	multi.Keywords = []string{"甲乙", "别名"}
	r := loadEntries(t, long, short, multi)
	if got := recallIDs(t, r, publicScope(), "甲乙丙 甲乙 甲乙 别名"); !reflect.DeepEqual(got, []string{"long", "multi", "short"}) {
		t.Fatal(got)
	}
	cat, generic := testEntry("cat", "cat"), testEntry("generic", "其他内容")
	tags := []string{"梗", "知识", "游戏", "文字梗", "游戏梗", "网络用语", "表情包", "直播", "自学习"}
	generic.Tags = &tags
	r = loadEntries(t, cat, generic)
	for _, q := range []string{"", "   ", "？？？", "concatenate", "游戏知识梗", "这是什么文字梗", "游戏梗网络用语", "表情包直播自学习", "不相关"} {
		if got := recallIDs(t, r, publicScope(), q); len(got) != 0 {
			t.Fatal(q, got)
		}
	}
	if got := recallIDs(t, r, publicScope(), "  CAT! "); !reflect.DeepEqual(got, []string{"cat"}) {
		t.Fatal(got)
	}
}

func TestRecallPermissionBeforeMatch(t *testing.T) {
	pub, alice, bob, qq, group := testEntry("public", "口令"), testEntry("alice", "口令"), testEntry("bob", "口令"), testEntry("qq", "口令"), testEntry("group", "口令")
	private := true
	alice.Visibility = "private"
	alice.Scope = &diskScope{Channel: event.ChDM, Session: "alice", Private: &private}
	alice.Scenes = []string{"dm"}
	bob.Visibility = "private"
	bob.Scope = &diskScope{Channel: event.ChDM, Session: "bob", Private: &private}
	bob.Scenes = []string{"dm"}
	qq.Visibility = "private"
	qq.Scope = &diskScope{Channel: event.ChQQ, Session: "alice", Private: &private}
	qq.Scenes = []string{"qq_private"}
	group.Scenes = []string{"qq_group"}
	r := loadEntries(t, alice, bob, qq, group, pub)
	tests := []struct {
		scope Scope
		ids   []string
	}{
		{publicScope(), []string{"public"}},
		{Scope{Channel: event.ChDM, Session: "alice", Private: true}, []string{"alice", "public"}},
		{Scope{Channel: event.ChDM, Session: "bob", Private: true}, []string{"bob", "public"}},
		{Scope{Channel: event.ChQQ, Session: "alice", Private: true}, []string{"public", "qq"}},
		{Scope{Channel: event.ChQQ, Session: "alice"}, []string{"group", "public"}},
	}
	for _, test := range tests {
		if got := recallIDs(t, r, test.scope, "口令"); !reflect.DeepEqual(got, test.ids) {
			t.Fatal(test.scope, got)
		}
	}
}

func TestRecallErrorsAndIndependentCopies(t *testing.T) {
	d := testEntry("private", "口令")
	private := true
	d.Visibility = "private"
	d.Scenes = []string{"dm"}
	d.Scope = &diskScope{Channel: event.ChDM, Session: "alice", Private: &private}
	r := loadEntries(t, d)
	q := Query{Scope: Scope{Channel: event.ChDM, Session: "alice", Private: true}, Text: "口令"}
	if _, err := r.Recall(nil, q); err == nil { //nolint:staticcheck // 专门验证非法nil输入的错误路径。
		t.Fatal("nil ctx未拒绝")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Recall(ctx, q); !errors.Is(err, context.Canceled) {
		t.Fatal("取消错误丢失")
	}
	for _, bad := range []Query{{Text: "口令"}, {Scope: publicScope(), Text: string([]byte{255})}} {
		if _, err := r.Recall(context.Background(), bad); err == nil {
			t.Fatal("非法查询未拒绝")
		}
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := r.Recall(context.Background(), q)
			if err != nil || len(c.Items) != 1 {
				t.Error("并发召回错误")
				return
			}
			c.Items[0].Scenes[0] = "live"
			c.Items[0].Origin.Session = "changed"
			c.Items[0].Text = "changed"
		}()
	}
	wg.Wait()
	if got := recallIDs(t, r, q.Scope, "口令"); !reflect.DeepEqual(got, []string{"private"}) {
		t.Fatal("返回值污染快照")
	}
}
