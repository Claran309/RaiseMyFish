package dm

// 本文件测 S10 每日收信人预占：并发不同 peer 只有 1 个通过、明确失败释放、
// 未知保留、成功后不可释放、actor 隔离与跨天重置。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/sender"
)

func newReserveSender(t *testing.T, path string, peers []string, clock *time.Time) (*Sender, *fakeTransport, *fakeRelation) {
	t.Helper()
	tr := &fakeTransport{}
	rel := &fakeRelation{attribute: mutualAttribute}
	s, err := New(tr, rel, Options{
		AllowedMIDs:   peers,
		RateLimit:     10 * time.Minute,
		Now:           func() time.Time { return *clock },
		RecipientPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, tr, rel
}

// TestRecipientStoreReserveIsAtomic 存储级并发：同一 actor/day 只有 1 个 peer 预占成功。
func TestRecipientStoreReserveIsAtomic(t *testing.T) {
	store, err := NewRecipientStore(filepath.Join(t.TempDir(), "dm-recipient.json"))
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var ok, locked, other atomic.Int32
	for i := 0; i < workers; i++ {
		peer := fmt.Sprintf("%d", 9000+i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch err := store.Reserve("2026-11-06", "111", peer); {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, errRecipientLocked):
				locked.Add(1)
			default:
				other.Add(1)
				t.Errorf("预占出现意外错误：%v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if ok.Load() != 1 || locked.Load() != workers-1 || other.Load() != 0 {
		t.Fatalf("并发预占结果 ok=%d locked=%d other=%d", ok.Load(), locked.Load(), other.Load())
	}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Peers) != 1 || st.Peers["111"] == "" {
		t.Fatalf("落盘应只有一个收信人：%+v", st)
	}
}

// TestRecipientStoreReleaseOnlyBeforeCommit 预占可在明确失败后释放；成功提交后不可释放。
func TestRecipientStoreReleaseOnlyBeforeCommit(t *testing.T) {
	store, err := NewRecipientStore(filepath.Join(t.TempDir(), "dm-recipient.json"))
	if err != nil {
		t.Fatal(err)
	}
	const day = "2026-11-06"
	if err := store.Reserve(day, "111", "1001"); err != nil {
		t.Fatal(err)
	}
	store.Release(day, "111", "1001")
	if err := store.Reserve(day, "111", "1002"); err != nil {
		t.Fatalf("释放后应允许换人：%v", err)
	}
	if err := store.Commit(day, "111", "1002"); err != nil {
		t.Fatal(err)
	}
	// 成功发送后，并发的失败尝试不能再把当天名额让给另一个人。
	store.Release(day, "111", "1002")
	if err := store.Reserve(day, "111", "1003"); !errors.Is(err, errRecipientLocked) {
		t.Fatalf("已提交的预占不应被释放：%v", err)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Peers["111"] != "1002" || st.Committed["111"] != "1002" {
		t.Fatalf("落盘不符：%+v", st)
	}
}

// TestRecipientStoreDayReset 跨天后预占与 committed 都重置。
func TestRecipientStoreDayReset(t *testing.T) {
	store, err := NewRecipientStore(filepath.Join(t.TempDir(), "dm-recipient.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve("2026-11-06", "111", "1001"); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit("2026-11-06", "111", "1001"); err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve("2026-11-06", "111", "1002"); !errors.Is(err, errRecipientLocked) {
		t.Fatalf("同日换人应拒绝：%v", err)
	}
	if err := store.Reserve("2026-11-07", "111", "1002"); err != nil {
		t.Fatalf("跨天应允许换人：%v", err)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Date != "2026-11-07" || st.Peers["111"] != "1002" || st.Committed["111"] != "" {
		t.Fatalf("跨天未重置：%+v", st)
	}
}

// TestRecipientStoreCommitOldDayDoesNotClobber 跨午夜的旧尝试提交不能回写旧日期。
func TestRecipientStoreCommitOldDayDoesNotClobber(t *testing.T) {
	store, err := NewRecipientStore(filepath.Join(t.TempDir(), "dm-recipient.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve("2026-11-06", "111", "1001"); err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve("2026-11-07", "111", "1002"); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit("2026-11-06", "111", "1001"); err != nil {
		t.Fatalf("旧日提交不应报错：%v", err)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Date != "2026-11-07" || st.Peers["111"] != "1002" {
		t.Fatalf("旧尝试覆盖了当天状态：%+v", st)
	}
}

// TestRecipientStoreReadsLegacyFormat 旧格式 {date, peer} 仍能挡住同日换人。
func TestRecipientStoreReadsLegacyFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dm-recipient.json")
	if err := os.WriteFile(path, []byte(`{"date":"2026-11-06","peer":"1001"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewRecipientStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve("2026-11-06", "", "1002"); !errors.Is(err, errRecipientLocked) {
		t.Fatalf("旧格式应视为已锁定：%v", err)
	}
	if err := store.Reserve("2026-11-06", "", "1001"); err != nil {
		t.Fatalf("旧格式同人应放行：%v", err)
	}
}

// concurrentTransport 供并发测试使用；计数与延迟必须并发安全。
type concurrentTransport struct {
	calls atomic.Int32
	delay time.Duration
}

func (t *concurrentTransport) SendDM(context.Context, string, string) (string, error) {
	t.calls.Add(1)
	if t.delay > 0 {
		time.Sleep(t.delay)
	}
	return "msg-1", nil
}

// TestRecipientConcurrentPeersOnlyOnePosts 不同 peer 并发，最多一个 POST。
func TestRecipientConcurrentPeersOnlyOnePosts(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	peers := []string{"1001", "1002", "1003", "1004"}
	tr := &concurrentTransport{delay: 20 * time.Millisecond}
	s, err := New(tr, &fakeRelation{attribute: mutualAttribute}, Options{
		AllowedMIDs:   peers,
		RateLimit:     10 * time.Minute,
		Now:           func() time.Time { return now },
		RecipientPath: filepath.Join(t.TempDir(), "dm-recipient.json"),
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, len(peers))
	for _, peer := range peers {
		go func(peer string) {
			<-start
			_, err := s.Send(context.Background(), claimFor(peer), sender.Reply{Text: "你好"})
			results <- err
		}(peer)
	}
	close(start)

	success := 0
	for range peers {
		if err := <-results; err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("并发不同 peer 应只有 1 个通过，实际 %d", success)
	}
	if got := tr.calls.Load(); got != 1 {
		t.Fatalf("只应产生 1 次 POST，实际 %d", got)
	}
}

// TestRecipientUnknownKeepsReservation 结果未知（可能已送达）时保留当天名额。
func TestRecipientUnknownKeepsReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dm-recipient.json")
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s, tr, _ := newReserveSender(t, path, []string{"1001", "1002"}, &now)
	tr.err = errors.New("network timeout")

	_, err := s.Send(context.Background(), claimFor("1001"), sender.Reply{Text: "可能已送达"})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindRetry {
		t.Fatalf("网络超时应归为 retry：%v", err)
	}
	store, storeErr := NewRecipientStore(path)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	st, loadErr := store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if st.Peers[""] != "1001" {
		t.Fatalf("未知结果应保留预占：%+v", st)
	}

	tr.err = nil
	if _, err := s.Send(context.Background(), claimFor("1002"), sender.Reply{Text: "换人"}); err == nil {
		t.Fatal("未知结果后当天换人应拒绝")
	}
	if tr.sent != 1 {
		t.Fatalf("拒绝时不应再 POST：%d", tr.sent)
	}
}

// TestRecipientReleasesBeforePost 限频/互关等 POST 前失败释放预占。
func TestRecipientReleasesBeforePost(t *testing.T) {
	cases := []struct {
		name string
		prep func(rel *fakeRelation)
	}{
		{"非互关", func(rel *fakeRelation) { rel.attribute = 2 }},
		{"关系查询失败", func(rel *fakeRelation) { rel.err = errors.New("relation down") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			s, tr, rel := newReserveSender(t, filepath.Join(t.TempDir(), "dm-recipient.json"), []string{"1001", "1002"}, &now)
			tc.prep(rel)
			if _, err := s.Send(context.Background(), claimFor("1001"), sender.Reply{Text: "失败"}); err == nil {
				t.Fatal("应拒绝")
			}
			if tr.sent != 0 {
				t.Fatalf("POST 前失败不应调用传输层：%d", tr.sent)
			}
			rel.attribute = mutualAttribute
			rel.err = nil
			if _, err := s.Send(context.Background(), claimFor("1002"), sender.Reply{Text: "换人"}); err != nil {
				t.Fatalf("明确失败后应释放预占：%v", err)
			}
		})
	}
}

// barrierRelation 让两笔同 peer 发送都完成预占后再分别返回失败/互关。
type barrierRelation struct {
	mu      sync.Mutex
	calls   int
	arrived chan struct{}
	release chan struct{}
}

func (r *barrierRelation) Attribute(context.Context, string) (int, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call <= 2 {
		r.arrived <- struct{}{}
		<-r.release
		if call == 1 {
			return 0, errors.New("relation down")
		}
	}
	return mutualAttribute, nil
}

// blockingTransport 让成功的 POST 停在 gate 上，便于观察在途窗口。
type blockingTransport struct {
	calls atomic.Int32
	gate  chan struct{}
}

func (t *blockingTransport) SendDM(context.Context, string, string) (string, error) {
	t.calls.Add(1)
	<-t.gate
	return "msg-1", nil
}

// TestRecipientInFlightFailureKeepsReservation 同 peer 并发时，失败方不能释放仍在途的预占。
func TestRecipientInFlightFailureKeepsReservation(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	rel := &barrierRelation{arrived: make(chan struct{}, 2), release: make(chan struct{})}
	tr := &blockingTransport{gate: make(chan struct{})}
	s, err := New(tr, rel, Options{
		AllowedMIDs:   []string{"1001", "1002"},
		RateLimit:     10 * time.Minute,
		Now:           func() time.Time { return now },
		RecipientPath: filepath.Join(t.TempDir(), "dm-recipient.json"),
	})
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 3)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := s.Send(context.Background(), claimFor("1001"), sender.Reply{Text: "同一人"})
			results <- err
		}()
	}
	for i := 0; i < 2; i++ {
		<-rel.arrived
	}
	close(rel.release)

	// 先返回的必然是关系查询失败的那笔；成功那笔仍卡在 POST。
	if err := <-results; err == nil {
		t.Fatal("先返回的尝试应失败")
	}

	go func() {
		_, err := s.Send(context.Background(), claimFor("1002"), sender.Reply{Text: "换人"})
		results <- err
	}()
	if err := <-results; err == nil {
		t.Fatal("在途发送仍持有预占，换人应拒绝")
	}
	if got := tr.calls.Load(); got != 1 {
		t.Fatalf("在途窗口内只应有 1 次 POST，实际 %d", got)
	}

	close(tr.gate)
	if err := <-results; err != nil {
		t.Fatalf("成功那笔应放行：%v", err)
	}
	if got := tr.calls.Load(); got != 1 {
		t.Fatalf("最终只应有 1 次 POST，实际 %d", got)
	}
}

// TestRecipientPerActor 同一人多个账号当天各自独立锁定；切号不会共用名额。
func TestRecipientPerActor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dm-recipient.json")
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s, tr, _ := newReserveSender(t, path, []string{"1001", "1002", "1003"}, &now)
	actorA := bili.WithActor(context.Background(), bili.Actor{AccountID: "a1", MID: "111", Cookie: "SESSDATA=test"})
	actorB := bili.WithActor(context.Background(), bili.Actor{AccountID: "a2", MID: "222", Cookie: "SESSDATA=test"})

	if _, err := s.Send(actorA, claimFor("1001"), sender.Reply{Text: "A 号"}); err != nil {
		t.Fatalf("账号 A 首发失败：%v", err)
	}
	if _, err := s.Send(actorB, claimFor("1002"), sender.Reply{Text: "B 号"}); err != nil {
		t.Fatalf("账号 B 应独立放行：%v", err)
	}
	if _, err := s.Send(actorA, claimFor("1003"), sender.Reply{Text: "A 号换人"}); err == nil {
		t.Fatal("账号 A 同日换人应拒绝")
	}
	if tr.sent != 2 {
		t.Fatalf("应只发出 2 条：%d", tr.sent)
	}

	store, err := NewRecipientStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Peers["111"] != "1001" || st.Peers["222"] != "1002" {
		t.Fatalf("actor 预占未隔离：%+v", st)
	}

	// 重启后身份隔离仍然生效。
	restarted, tr2, _ := newReserveSender(t, path, []string{"1001", "1002", "1003"}, &now)
	if _, err := restarted.Send(actorA, claimFor("1003"), sender.Reply{Text: "重启后换人"}); err == nil {
		t.Fatal("重启后账号 A 同日换人应拒绝")
	}
	if tr2.sent != 0 {
		t.Fatalf("重启后拒绝不应发送：%d", tr2.sent)
	}
}
