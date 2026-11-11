package dm

// 本文件是 S09/T18 的切号归属测试：
//   - 一批采集开始后切换活跃账号，本批 list/detail 仍全部使用旧账号凭据；
//   - Batch 的 AccountID/SelfID 固定为批次开始时的快照；
//   - 水位按账号隔离，旧账号观察不写进新账号；
//   - Batch.Close 释放账号切换准入。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/bili"
)

// switchActors 是可并发修改的账号快照源，模拟面板在采集中途切号。
type switchActors struct {
	mu    sync.Mutex
	actor bili.Actor
}

func (s *switchActors) CurrentActor(context.Context) (bili.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.actor, nil
}

func (s *switchActors) set(actor bili.Actor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actor = actor
}

// countingAdmission 记录准入/释放次数，验证批次生命周期。
type countingAdmission struct {
	mu       sync.Mutex
	begins   int
	releases int
}

func (a *countingAdmission) BeginOperation(context.Context) (func(), error) {
	a.mu.Lock()
	a.begins++
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.releases++
			a.mu.Unlock()
		})
	}, nil
}

func (a *countingAdmission) counts() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.begins, a.releases
}

const oldCookie = "SESSDATA=old-secret; bili_jct=csrf-old"
const newCookie = "SESSDATA=new-secret; bili_jct=csrf-new"

func actor(accountID, mid, cookie string) bili.Actor {
	return bili.Actor{AccountID: accountID, MID: mid, Cookie: cookie, CSRF: "csrf", Source: "db"}
}

// switchingFixture 包装 fakeDM：记录每个请求的 Cookie，并支持在第一个会话页后切号。
type switchingFixture struct {
	*fakeDM
	mu       sync.Mutex
	cookies  []string
	onSwitch func()
}

func (f *switchingFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.cookies = append(f.cookies, r.Header.Get("Cookie"))
	first := len(f.cookies) == 1 && r.URL.Path == "/session_svr/v1/session_svr/get_sessions"
	hook := f.onSwitch
	f.mu.Unlock()
	f.fakeDM.ServeHTTP(w, r)
	if first && hook != nil {
		hook()
	}
}

func (f *switchingFixture) recordedCookies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cookies...)
}

func newSwitchingClient(t *testing.T, fake *switchingFixture, defaultCookie, defaultMID string) *bili.Client {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := bili.New(bili.Options{
		APIRoot: srv.URL, // nav（WBI 密钥）也走 mock
		VCRoot:  srv.URL,
		Cookie:  defaultCookie,
		SelfMID: defaultMID,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造 bili 客户端失败：%v", err)
	}
	return client
}

func seedSessions(fake *fakeDM, count int) {
	for i := 0; i < count; i++ {
		peer := strconv.Itoa(4000 + i)
		fake.sessions = append(fake.sessions, fakeSession{talker: peer, ts: int64(1700010000 - i)})
		fake.messages[peer] = []fakeMessage{
			{key: peer + "-1", sender: peer, text: "来自" + peer, seq: 1},
			{key: peer + "-2", sender: "111", text: "旧号自己发的", seq: 2},
		}
	}
}

// TestBiliFetcherKeepsActorForWholeBatch 采集中途切号：list/detail 全部用旧凭据与旧 self_id。
func TestBiliFetcherKeepsActorForWholeBatch(t *testing.T) {
	fake := &switchingFixture{fakeDM: newFakeDM()}
	seedSessions(fake.fakeDM, 4)

	actors := &switchActors{actor: actor("account-old", "111", oldCookie)}
	fake.onSwitch = func() { actors.set(actor("account-new", "222", newCookie)) }
	admission := &countingAdmission{}

	// 底层客户端已经解析到新账号：批次若不做绑定就会读到新号。
	client := newSwitchingClient(t, fake, newCookie, "222")
	store := NewMemoryCursorStore()
	f := pagedFetcher(client, store, 2, 20, 5, 3)
	f.Actors = actors
	f.Admission = admission
	f.SelfID = "222"

	batch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("采集失败：%v", err)
	}
	if batch.AccountID != "account-old" || batch.SelfID != "111" {
		t.Fatalf("批次账号快照不符：account=%q self=%q", batch.AccountID, batch.SelfID)
	}
	batches, releases := admission.counts()
	if batches != 1 || releases != 0 {
		t.Fatalf("整批处理结束前不应释放准入：begins=%d releases=%d", batches, releases)
	}

	for _, cookie := range fake.recordedCookies() {
		if cookie != oldCookie {
			t.Fatalf("切号后请求串用了新凭据：%q", cookie)
		}
	}
	if len(fake.recordedCookies()) != 8 {
		t.Fatalf("应发 3 次会话列表 + 4 次详情 + 1 次 nav，实际 %d", len(fake.recordedCookies()))
	}
	for _, msg := range batch.Messages {
		if msg.ID == "" || msg.PeerID == "" {
			t.Fatalf("消息不完整：%+v", msg)
		}
	}
	// 旧号的自己的消息必须按旧 self_id 过滤；新号身份不参与本批归属。
	for _, msg := range batch.Messages {
		if msg.Text == "旧号自己发的" {
			t.Fatalf("旧号自己的消息不应产出：%+v", msg)
		}
	}
	if len(batch.Messages) != 4 {
		t.Fatalf("4 个会话各一条对方消息，实际 %d：%+v", len(batch.Messages), batch.Messages)
	}

	if batch.Commit == nil {
		t.Fatal("有水位变化时必须提供 Commit")
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("提交水位失败：%v", err)
	}
	if saved, _ := store.Load("111"); len(saved) != 4 {
		t.Errorf("水位应写在旧账号下：%+v", saved)
	}
	if saved, _ := store.Load("222"); len(saved) != 0 {
		t.Errorf("新账号不应继承旧账号水位：%+v", saved)
	}

	batch.Close()
	_, releases = admission.counts()
	if releases != 1 {
		t.Fatalf("Close 应释放一次准入：%d", releases)
	}
	batch.Close()
	_, releases = admission.counts()
	if releases != 1 {
		t.Fatalf("Close 重复调用不应重复释放：%d", releases)
	}
}

// TestBiliFetcherSwitchesAccountBetweenBatches 切号后的下一批使用新账号，水位互不污染。
func TestBiliFetcherSwitchesAccountBetweenBatches(t *testing.T) {
	fake := &switchingFixture{fakeDM: newFakeDM()}
	seedSessions(fake.fakeDM, 1)
	actors := &switchActors{actor: actor("account-old", "111", oldCookie)}
	client := newSwitchingClient(t, fake, oldCookie, "111")
	store := NewMemoryCursorStore()
	f := pagedFetcher(client, store, 20, 20, 5, 3)
	f.Actors = actors

	first, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("旧账号批次失败：%v", err)
	}
	if first.AccountID != "account-old" || first.SelfID != "111" {
		t.Fatalf("旧批次快照不符：%+v", first)
	}
	if first.Commit == nil {
		t.Fatalf("Fetch 应返回可提交批次：%+v", first)
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	first.Close()

	// 切号：同一 Fetcher 重建内存态，水位与 lastKeys 都按账号隔离。
	actors.set(actor("account-new", "222", newCookie))
	second, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("新账号批次失败：%v", err)
	}
	if second.AccountID != "account-new" || second.SelfID != "222" {
		t.Fatalf("新批次快照不符：%+v", second)
	}
	if second.Commit == nil {
		t.Fatal("新账号首次接入必须建立基线")
	}
	if err := second.Commit(); err != nil {
		t.Fatal(err)
	}
	second.Close()

	cookies := fake.recordedCookies()
	if cookies[0] != oldCookie || cookies[len(cookies)-1] != newCookie {
		t.Fatalf("切号前后凭据未按批次切换：%v", cookies)
	}
	if saved, _ := store.Load("111"); len(saved) != 1 {
		t.Errorf("旧账号水位应保留：%+v", saved)
	}
	if saved, _ := store.Load("222"); len(saved) != 1 {
		t.Errorf("新账号应独立建立水位：%+v", saved)
	}
}

// TestBiliFetcherAdmissionFailureStopsBatch 切号屏障拒绝准入时整轮不发请求。
func TestBiliFetcherAdmissionFailureStopsBatch(t *testing.T) {
	fake := &switchingFixture{fakeDM: newFakeDM()}
	seedSessions(fake.fakeDM, 1)
	actors := &switchActors{actor: actor("account-old", "111", oldCookie)}
	client := newSwitchingClient(t, fake, oldCookie, "111")
	f := pagedFetcher(client, NewMemoryCursorStore(), 20, 20, 5, 3)
	f.Actors = actors
	f.Admission = denyingAdmission{}

	_, err := f.Fetch(context.Background())
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindNetwork {
		t.Fatalf("准入失败应归一为网络类错误：%v", err)
	}
	if len(fake.recordedCookies()) != 0 {
		t.Fatal("未取得准入仍然发了请求")
	}
}

// TestBiliFetcherActorUnavailableStopsBatch 当前没有可用账号时按鉴权错误停采集。
func TestBiliFetcherActorUnavailableStopsBatch(t *testing.T) {
	fake := &switchingFixture{fakeDM: newFakeDM()}
	seedSessions(fake.fakeDM, 1)
	client := newSwitchingClient(t, fake, oldCookie, "111")
	f := pagedFetcher(client, NewMemoryCursorStore(), 20, 20, 5, 3)
	f.Actors = noCredentialActors{}

	_, err := f.Fetch(context.Background())
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindAuth {
		t.Fatalf("无凭据应归一为鉴权错误：%v", err)
	}
	if len(fake.recordedCookies()) != 0 {
		t.Fatal("无凭据仍然发了请求")
	}
}

type denyingAdmission struct{}

func (denyingAdmission) BeginOperation(context.Context) (func(), error) {
	return nil, errors.New("切换中")
}

type noCredentialActors struct{}

func (noCredentialActors) CurrentActor(context.Context) (bili.Actor, error) {
	return bili.Actor{}, bili.ErrNoCredential
}

// invalidActorSource 返回缺少 cookie 的快照；不能退回动态客户端读新号。
type invalidActorSource struct{}

func (invalidActorSource) CurrentActor(context.Context) (bili.Actor, error) {
	return bili.Actor{AccountID: "account-old", MID: "111"}, nil
}

func TestBiliFetcherInvalidActorNeverFallsBack(t *testing.T) {
	fake := &switchingFixture{fakeDM: newFakeDM()}
	seedSessions(fake.fakeDM, 1)
	client := newSwitchingClient(t, fake, oldCookie, "111")
	f := pagedFetcher(client, NewMemoryCursorStore(), 20, 20, 5, 3)
	f.Actors = invalidActorSource{}

	_, err := f.Fetch(context.Background())
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindAuth {
		t.Fatalf("无效快照应归一为鉴权错误：%v", err)
	}
	if len(fake.recordedCookies()) != 0 {
		t.Fatal("无效快照仍然退回了动态客户端")
	}
}
