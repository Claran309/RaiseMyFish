package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

type memeTestSource struct {
	events  []event.Event
	channel event.Channel
	since   time.Time
	limit   int
}

func (s *memeTestSource) PublicSince(_ context.Context, ch event.Channel, since time.Time, limit int) ([]event.Event, error) {
	s.channel, s.since, s.limit = ch, since, limit
	return s.events, nil
}

type memeTestReader struct {
	items []Item
	// terms 是「精确词表」：梗白学习只用它判断是否已收录。
	terms []string
	// recallCalls 记录 Recall 调用次数：M09 之后必须恒为 0。
	recallCalls int
}

func (r *memeTestReader) Recall(_ context.Context, query Query) (Context, error) {
	r.recallCalls++
	return Context{Scope: query.Scope, Revision: strings.Repeat("a", 64), Items: r.items}, nil
}

func (r *memeTestReader) ExactTerms(context.Context) ([]string, error) {
	return r.terms, nil
}

type memeTestLLM struct {
	mu    sync.Mutex
	calls int
	reply func(user string) (string, error)
}

func (f *memeTestLLM) Complete(_ context.Context, _, user string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.reply(user)
}

func (f *memeTestLLM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func memeEvent(id, text string, at time.Time, private bool) event.Event {
	return event.Event{
		ID: id, Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "uid-1", FromName: "路人", Text: text, Private: private,
		At: at, Status: event.StatusPending,
	}
}

func candidateOf(user string) string {
	const prefix = "候选："
	start := strings.Index(user, prefix)
	if start < 0 {
		return ""
	}
	rest := user[start+len(prefix):]
	if end := strings.IndexByte(rest, '\n'); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

func learnReply(title string) string {
	data, _ := json.Marshal(memeLearnReply{
		Learn: true,
		Title: title,
		Usage: "看到弹幕刷这个时就跟着玩梗",
		Tags:  []string{"弹幕梗", "直播间"},
	})
	return string(data)
}

func learnEveryCandidate() func(string) (string, error) {
	return func(user string) (string, error) { return learnReply(candidateOf(user)), nil }
}

func TestExtractMemeCandidates(t *testing.T) {
	now := time.Now().UTC()
	events := []event.Event{
		memeEvent("e1", "蚌埠住了 蚌埠住了", now, false),
		memeEvent("e2", "@小明 蚌埠住了", now, false),
		memeEvent("e3", "666 666", now, false),
		memeEvent("e4", "单次词", now, false),
		memeEvent("e5", "！！！", now, false),
		memeEvent("e6", "https://example.com/蚌埠住了", now, false),
		memeEvent("e7", "私密词 私密词", now, true),
		memeEvent("e8", "hello hello", now, false),
	}
	got := extractMemeCandidates(events, 2, 10)
	byText := make(map[string]memeCandidate, len(got))
	for _, cand := range got {
		byText[cand.text] = cand
	}
	if len(got) != 2 {
		t.Fatalf("候选数量 = %d，期望 2：%+v", len(got), got)
	}
	if cand, ok := byText["蚌埠住了"]; !ok || cand.count != 3 || len(cand.samples) != 2 {
		t.Fatalf("重复梗候选不符：%+v", byText)
	}
	if cand, ok := byText["hello"]; !ok || cand.count != 2 {
		t.Fatalf("英文整词候选不符：%+v", byText)
	}
	for _, bad := range []string{"666", "单次词", "私密词", "小明", "example"} {
		if _, ok := byText[bad]; ok {
			t.Fatalf("不应出现候选 %q：%+v", bad, got)
		}
	}
}

// hotTestWords 是 20 个互不包含的英文整词，避免 n-gram 与包含去重干扰排序断言。
var hotTestWords = []string{
	"alpha", "bravo", "charlie", "delta", "echo",
	"foxtrot", "golf", "hotel", "india", "juliet",
	"kilo", "lima", "mike", "november", "oscar",
	"papa", "quebec", "romeo", "sierra", "tango",
}

// hotTestEvents 让第 i 个词出现 20-i 次，构造严格的次数降序。
func hotTestEvents(now time.Time) []event.Event {
	events := make([]event.Event, 0, len(hotTestWords))
	for i, word := range hotTestWords {
		events = append(events, memeEvent(fmt.Sprintf("hot-%d", i), strings.Repeat(word+" ", 20-i), now, false))
	}
	return events
}

// TestExtractMemeCandidatesTopHot15 候选按出现次数降序，且只保留前 15。
func TestExtractMemeCandidatesTopHot15(t *testing.T) {
	got := extractMemeCandidates(hotTestEvents(time.Now().UTC()), 1, 15)
	if len(got) != 15 {
		t.Fatalf("候选数量 = %d，期望 15：%+v", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].count < got[i].count {
			t.Fatalf("候选未按出现次数降序：%+v", got)
		}
	}
	for i, want := range hotTestWords[:15] {
		if got[i].text != want {
			t.Fatalf("第 %d 个候选 = %q，期望 %q：%+v", i, got[i].text, want, got)
		}
	}
	if got[0].count != 20 || got[14].count != 6 {
		t.Fatalf("Top15 次数不符：first=%d last=%d", got[0].count, got[14].count)
	}
	for _, cand := range got {
		if cand.text == hotTestWords[15] {
			t.Fatalf("第 16 名不应进入 Top15：%+v", cand)
		}
	}
}

func TestCandidateShape(t *testing.T) {
	cases := map[string]bool{
		"蚌埠住了":      true,
		"ab":        true,
		"a1":        true,
		"666":       false,
		"哈哈哈哈":      false,
		"a":         false,
		"一二三四五六七八九": false,
		"a-b":       false,
		" ":         false,
	}
	for text, want := range cases {
		if got := candidateShapeOK(text); got != want {
			t.Fatalf("candidateShapeOK(%q) = %v，期望 %v", text, got, want)
		}
	}
}

func TestMemeLearnerWritesAndIsIdempotent(t *testing.T) {
	dir := knowledgeDir(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	events := []event.Event{
		memeEvent("e1", "蚌埠住了", now.Add(-2*time.Minute), false),
		memeEvent("e2", "蚌埠住了", now.Add(-time.Minute), false),
	}
	source := &memeTestSource{events: events}
	llm := &memeTestLLM{reply: learnEveryCandidate()}
	learner, err := NewMemeLearner(dir, &memeTestReader{}, source, llm, MemeLearnOptions{
		Window:    6 * time.Hour,
		MaxPerDay: 10,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := learner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned != 2 || stats.Candidates != 1 || stats.Learned != 1 {
		t.Fatalf("统计不符：%+v", stats)
	}
	if source.channel != event.ChLive || source.limit != maxMemeScanEvents || !source.since.Equal(now.Add(-6*time.Hour)) {
		t.Fatalf("查询窗口不符：%+v", source)
	}
	data, err := os.ReadFile(filepath.Join(dir, "memes", "learned.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"蚌埠住了", "meme-learn:auto", "meme-learned-20261103-"} {
		if !strings.Contains(text, want) {
			t.Fatalf("learned.md 缺少 %q：%s", want, text)
		}
	}
	reader, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatalf("写入的文件未通过 K0 校验：%v", err)
	}
	got, err := reader.Recall(context.Background(), Query{Scope: Scope{Channel: event.ChLive, Session: "room"}, Text: "蚌埠住了"})
	if err != nil || len(got.Items) != 1 {
		t.Fatalf("学到的梗未参与召回：%v %+v", err, got.Items)
	}

	callsBefore := llm.count()
	again, err := learner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.Learned != 0 || again.Duplicated != 1 {
		t.Fatalf("重复学习未幂等：%+v", again)
	}
	if llm.count() != callsBefore {
		t.Fatalf("已学标题仍调用了模型：%d -> %d", callsBefore, llm.count())
	}
	info, err := reader.SnapshotInfo()
	if err != nil || info.Entries != 1 {
		t.Fatalf("快照条目数不符：%+v %v", info, err)
	}
}

func TestMemeLearnerDailyLimit(t *testing.T) {
	dir := knowledgeDir(t)
	now := time.Date(2026, 11, 3, 23, 0, 0, 0, time.UTC)
	events := []event.Event{
		memeEvent("a1", "梗甲", now, false),
		memeEvent("a2", "梗甲", now, false),
		memeEvent("b1", "梗乙", now, false),
		memeEvent("b2", "梗乙", now, false),
		memeEvent("c1", "梗丙", now, false),
		memeEvent("c2", "梗丙", now, false),
	}
	llm := &memeTestLLM{reply: learnEveryCandidate()}
	learner, err := NewMemeLearner(dir, &memeTestReader{}, &memeTestSource{events: events}, llm, MemeLearnOptions{
		MaxPerDay: 2,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := learner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Learned != 2 || stats.Limited != 1 || llm.count() != 2 {
		t.Fatalf("每日限流不符：stats=%+v calls=%d", stats, llm.count())
	}
	reader, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatalf("第二次写入破坏了文件：%v", err)
	}
	info, err := reader.SnapshotInfo()
	if err != nil || info.Entries != 2 {
		t.Fatalf("文件条目数不符：%+v %v", info, err)
	}

	callsBefore := llm.count()
	again, err := learner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.Learned != 0 || again.Limited != 1 || llm.count() != callsBefore {
		t.Fatalf("同日再次学习未受限：stats=%+v calls=%d->%d", again, callsBefore, llm.count())
	}
}

// TestMemeLearnerHot15UnlimitedDaily Top15 截断；MaxPerDay=0 时不限每日（可超过旧默认 10 条）。
func TestMemeLearnerHot15UnlimitedDaily(t *testing.T) {
	dir := knowledgeDir(t)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	llm := &memeTestLLM{reply: learnEveryCandidate()}
	learner, err := NewMemeLearner(dir, &memeTestReader{}, &memeTestSource{events: hotTestEvents(now)}, llm, MemeLearnOptions{
		MinCount:  1,
		HotLimit:  15,
		MaxPerDay: 0,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := learner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Candidates != 15 || stats.Learned != 15 || stats.Limited != 0 || llm.count() != 15 {
		t.Fatalf("Top15/不限每日不符：stats=%+v calls=%d", stats, llm.count())
	}
	data, err := os.ReadFile(filepath.Join(dir, "memes", "learned.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, hotTestWords[0]) {
		t.Fatalf("最高频候选未学习：%s", text)
	}
	if strings.Contains(text, hotTestWords[15]) {
		t.Fatalf("第 16 名之后的候选不应学习：%s", text)
	}
	reader, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatalf("写入的文件未通过 K0 校验：%v", err)
	}
	info, err := reader.SnapshotInfo()
	if err != nil || info.Entries != 15 {
		t.Fatalf("学习条数不符：%+v %v", info, err)
	}
}

func TestMemeLearnerSkipsKnownKnowledge(t *testing.T) {
	dir := knowledgeDir(t)
	now := time.Now().UTC()
	events := []event.Event{
		memeEvent("e1", "老梗", now, false),
		memeEvent("e2", "老梗", now, false),
	}
	reader := &memeTestReader{terms: []string{"老梗"}}
	llm := &memeTestLLM{reply: learnEveryCandidate()}
	learner, err := NewMemeLearner(dir, reader, &memeTestSource{events: events}, llm, MemeLearnOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := learner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Duplicated != 1 || stats.Learned != 0 || llm.count() != 0 {
		t.Fatalf("已有知识未跳过：stats=%+v calls=%d", stats, llm.count())
	}
	if _, err := os.Stat(filepath.Join(dir, "memes", "learned.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("没有学习时不应创建 learned.md：%v", err)
	}
}

// TestMemeLearnSemanticCloseButDistinctIsLearned T15：语义相近但不是同一个梗 → 必须收录
//
// 回归背景：旧实现用 Recall（相关性检索）判断「已收录」，
// 知识库里有语义相近的「绷不住了」时，新梗「蚌埠住了」会被误判成重复而永久丢失。
// 这里刻意让 Recall 仍返回「相近条目」，验证它不再参与去重。
func TestMemeLearnSemanticCloseButDistinctIsLearned(t *testing.T) {
	dir := knowledgeDir(t)
	now := time.Now().UTC()
	events := []event.Event{
		memeEvent("s1", "蚌埠住了", now, false),
		memeEvent("s2", "蚌埠住了", now, false),
	}
	reader := &memeTestReader{
		terms: []string{"绷不住了"}, // 规范词不同
		items: []Item{{ID: "near", Source: "test", Text: "主题：绷不住了", Scenes: []string{"live"}, Priority: 50}},
	}
	llm := &memeTestLLM{reply: learnEveryCandidate()}
	learner, err := NewMemeLearner(dir, reader, &memeTestSource{events: events}, llm, MemeLearnOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	stats, err := learner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Learned != 1 || stats.Duplicated != 0 {
		t.Fatalf("语义相近的不同梗应被收录：%+v", stats)
	}
	if reader.recallCalls != 0 {
		t.Fatalf("去重不应再用 Recall（调用 %d 次）", reader.recallCalls)
	}
	if _, err := os.Stat(filepath.Join(dir, "memes", "learned.md")); err != nil {
		t.Fatalf("新梗未落盘：%v", err)
	}
}

// TestMemeLearnExactTermIsDuplicated T15：规范词字面相同才算重复。
func TestMemeLearnExactTermIsDuplicated(t *testing.T) {
	dir := knowledgeDir(t)
	now := time.Now().UTC()
	events := []event.Event{
		memeEvent("d1", "蚌埠住了", now, false),
		memeEvent("d2", "蚌埠住了", now, false),
	}
	reader := &memeTestReader{terms: []string{"蚌埠住了"}}
	llm := &memeTestLLM{reply: learnEveryCandidate()}
	learner, err := NewMemeLearner(dir, reader, &memeTestSource{events: events}, llm, MemeLearnOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	stats, err := learner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Duplicated != 1 || stats.Learned != 0 {
		t.Fatalf("同名精确命中应判重复：%+v", stats)
	}
	if llm.count() != 0 {
		t.Fatalf("已收录的梗不该再问模型，实际 %d 次", llm.count())
	}
}

// plainTestReader 只实现 Reader，没有精确词表能力。
type plainTestReader struct{ recallCalls int }

func (r *plainTestReader) Recall(_ context.Context, query Query) (Context, error) {
	r.recallCalls++
	return Context{Scope: query.Scope}, nil
}

// TestMemeLearnRequiresExactTermSource 不支持精确词表时明确报错，不退回 Recall。
func TestMemeLearnRequiresExactTermSource(t *testing.T) {
	dir := knowledgeDir(t)
	now := time.Now().UTC()
	events := []event.Event{
		memeEvent("p1", "蚌埠住了", now, false),
		memeEvent("p2", "蚌埠住了", now, false),
	}
	reader := &plainTestReader{}
	llm := &memeTestLLM{reply: learnEveryCandidate()}
	learner, err := NewMemeLearner(dir, reader, &memeTestSource{events: events}, llm, MemeLearnOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := learner.RunOnce(context.Background()); err == nil {
		t.Fatal("缺少精确词表能力时应报错，而不是静默退回相关性检索")
	}
	if reader.recallCalls != 0 {
		t.Fatalf("不应退回 Recall，实际调用 %d 次", reader.recallCalls)
	}
}

func TestMemeLearnerSkipsBadModelReplies(t *testing.T) {
	dir := knowledgeDir(t)
	now := time.Now().UTC()
	events := []event.Event{
		memeEvent("a1", "梗甲", now, false),
		memeEvent("a2", "梗甲", now, false),
		memeEvent("b1", "梗乙", now, false),
		memeEvent("b2", "梗乙", now, false),
		memeEvent("c1", "梗丙", now, false),
		memeEvent("c2", "梗丙", now, false),
	}
	llm := &memeTestLLM{reply: func(user string) (string, error) {
		switch candidateOf(user) {
		case "梗甲":
			return "这不是 JSON", nil
		case "梗乙":
			return `{"learn":false}`, nil
		default:
			return learnReply(candidateOf(user)), nil
		}
	}}
	learner, err := NewMemeLearner(dir, &memeTestReader{}, &memeTestSource{events: events}, llm, MemeLearnOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := learner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 2 || stats.Learned != 1 || llm.count() != 3 {
		t.Fatalf("坏模型输出未按候选跳过：stats=%+v calls=%d", stats, llm.count())
	}

	callsBefore := llm.count()
	again, err := learner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.Skipped != 2 || again.Learned != 0 || llm.count() != callsBefore {
		t.Fatalf("被拒候选未缓存：stats=%+v calls=%d->%d", again, callsBefore, llm.count())
	}
}

func TestMemeLearnerRejectsOversizeFile(t *testing.T) {
	dir := knowledgeDir(t)
	writeKnowledge(t, dir, "memes/learned.md", strings.Repeat("a", maxFileBytes+1))
	now := time.Now().UTC()
	llm := &memeTestLLM{reply: learnEveryCandidate()}
	learner, err := NewMemeLearner(dir, &memeTestReader{}, &memeTestSource{events: []event.Event{
		memeEvent("e1", "蚌埠住了", now, false),
		memeEvent("e2", "蚌埠住了", now, false),
	}}, llm, MemeLearnOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := learner.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "单文件上限") {
		t.Fatalf("超大文件未拒绝：%v", err)
	}
	if llm.count() != 0 {
		t.Fatal("文件不可写时仍调用了模型")
	}
}

func TestNewMemeLearnerValidatesOptions(t *testing.T) {
	now := time.Now
	base := MemeLearnOptions{Now: now}
	if _, err := NewMemeLearner("", &memeTestReader{}, &memeTestSource{}, &memeTestLLM{}, base); err == nil {
		t.Fatal("空目录未拒绝")
	}
	if _, err := NewMemeLearner(t.TempDir(), nil, &memeTestSource{}, &memeTestLLM{}, base); err == nil {
		t.Fatal("nil Reader 未拒绝")
	}
	tests := map[string]MemeLearnOptions{
		"窗口过短":   {Window: time.Minute, Now: now},
		"窗口过长":   {Window: 8 * 24 * time.Hour, Now: now},
		"最低次数过大": {MinCount: 21, Now: now},
		"候选过多":   {HotLimit: 51, Now: now},
		"每日过多":   {MaxPerDay: 101, Now: now},
		"坏通道":    {Channel: "bad", Now: now},
	}
	for name, opts := range tests {
		if _, err := NewMemeLearner(t.TempDir(), &memeTestReader{}, &memeTestSource{}, &memeTestLLM{}, opts); err == nil {
			t.Fatalf("%s 未拒绝", name)
		}
	}
}
