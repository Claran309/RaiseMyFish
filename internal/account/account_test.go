package account

// 本文件用内存 Store/Verifier 测账号 CRUD、验证与凭据解析链（不发真实网络）。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/bili"
)

const dbCookie = "SESSDATA=db-secret; bili_jct=csrf-db"
const envCookie = "SESSDATA=env-secret; bili_jct=csrf-env"

type fakeStore struct {
	accounts   map[string]Account
	activeErr  error
	listErr    error
	upsertErr  error
	updateErr  error
	updates    []checkUpdate
	upsertCall int
	// strictContext 为 true 时，SetActive/UpdateCheck 像真实驱动一样拒绝已过期 ctx。
	// 用于证明验证失败后的记账使用的是独立有界 cleanup ctx。
	strictContext bool
	expiredCalls  []string
}

type checkUpdate struct {
	id, name, status string
	at               time.Time
}

func newFakeStore(items ...Account) *fakeStore {
	fs := &fakeStore{accounts: make(map[string]Account)}
	for _, item := range items {
		if item.ID == "" {
			item.ID = IDForMID(item.MID)
		}
		fs.accounts[item.ID] = item
	}
	return fs
}

func (f *fakeStore) List(context.Context) ([]Account, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]Account, 0, len(f.accounts))
	for _, item := range f.accounts {
		out = append(out, item)
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, id string) (Account, error) {
	item, ok := f.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return item, nil
}

func (f *fakeStore) Active(context.Context) (Account, error) {
	if f.activeErr != nil {
		return Account{}, f.activeErr
	}
	for _, item := range f.accounts {
		if item.IsActive {
			return item, nil
		}
	}
	return Account{}, ErrNotFound
}

func (f *fakeStore) Upsert(_ context.Context, a Account) (Account, error) {
	if f.upsertErr != nil {
		return Account{}, f.upsertErr
	}
	f.upsertCall++
	if a.ID == "" {
		a.ID = IDForMID(a.MID)
	}
	hasActive := false
	for _, item := range f.accounts {
		if item.IsActive {
			hasActive = true
			break
		}
	}
	if old, ok := f.accounts[a.ID]; ok {
		a.IsActive = old.IsActive
		a.CreatedAt = old.CreatedAt
	} else if !hasActive {
		a.IsActive = true
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	a.UpdatedAt = time.Now()
	f.accounts[a.ID] = a
	return a, nil
}

func (f *fakeStore) SetActive(ctx context.Context, id string) error {
	if f.rejectExpired(ctx, "SetActive") {
		return ctx.Err()
	}
	target, ok := f.accounts[id]
	if !ok {
		return ErrNotFound
	}
	for key, item := range f.accounts {
		item.IsActive = key == id
		f.accounts[key] = item
	}
	target.IsActive = true
	f.accounts[id] = target
	return nil
}

// Delete 与生产实现同语义（O10）：条件删除，活跃账号拒绝并返回 ErrActiveAccount。
func (f *fakeStore) Delete(_ context.Context, id string) error {
	stored, ok := f.accounts[id]
	if !ok {
		return ErrNotFound
	}
	if stored.IsActive {
		return ErrActiveAccount
	}
	delete(f.accounts, id)
	return nil
}

func (f *fakeStore) UpdateCheck(ctx context.Context, id, name, status string, at time.Time) error {
	if f.rejectExpired(ctx, "UpdateCheck") {
		return ctx.Err()
	}
	if f.updateErr != nil {
		return f.updateErr
	}
	item, ok := f.accounts[id]
	if !ok {
		return ErrNotFound
	}
	f.updates = append(f.updates, checkUpdate{id: id, name: name, status: status, at: at})
	item.Name = name
	item.Status = status
	item.LastCheck = at
	f.accounts[id] = item
	return nil
}

// rejectExpired 模拟真实驱动对已取消/超时 ctx 的处理，并记录违规调用。
func (f *fakeStore) rejectExpired(ctx context.Context, op string) bool {
	if !f.strictContext || ctx == nil || ctx.Err() == nil {
		return false
	}
	f.expiredCalls = append(f.expiredCalls, op)
	return true
}

type fakeVerifier struct {
	info  Identity
	err   error
	calls []string
}

func (f *fakeVerifier) Verify(_ context.Context, cookie string) (Identity, error) {
	f.calls = append(f.calls, cookie)
	return f.info, f.err
}

func newTestService(t *testing.T, store Store, verify Verifier, env string) *Service {
	t.Helper()
	svc, err := NewService(Options{
		Store:     store,
		Verifier:  verify,
		EnvCookie: env,
		Now:       func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestResolveChain DB 活跃账号优先，缺失时回退环境变量。
func TestResolveChain(t *testing.T) {
	stored := Account{ID: "account-db", MID: "42", Cookie: dbCookie, Status: StatusActive, IsActive: true}
	svc := newTestService(t, newFakeStore(stored), &fakeVerifier{}, envCookie)

	cred, err := svc.Resolve(context.Background())
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cred.Source != SourceDB || cred.MID != "42" || cred.AccountID != "account-db" {
		t.Errorf("未命中 DB 活跃账号：%+v", cred)
	}
	if cred.Cookie != dbCookie || cred.CSRF != "csrf-db" {
		t.Errorf("DB 凭据不符：%+v", cred)
	}

	// 没有活跃账号 → 环境兜底
	svc2 := newTestService(t, newFakeStore(), &fakeVerifier{}, envCookie)
	cred, err = svc2.Resolve(context.Background())
	if err != nil {
		t.Fatalf("环境兜底失败：%v", err)
	}
	if cred.Source != SourceEnv || cred.Cookie != envCookie || cred.CSRF != "csrf-env" || cred.MID != "" {
		t.Errorf("环境凭据不符：%+v", cred)
	}

	// 都没有 → 哨兵错误
	svc3 := newTestService(t, newFakeStore(), &fakeVerifier{}, "")
	if _, err := svc3.Resolve(context.Background()); !errors.Is(err, ErrNoCredential) {
		t.Errorf("应返回 ErrNoCredential，实际 %v", err)
	}
	cookie, mid, err := svc3.BiliCredential(context.Background())
	if !errors.Is(err, bili.ErrNoCredential) || cookie != "" || mid != "" {
		t.Errorf("BiliCredential 应映射为 bili.ErrNoCredential：%q %q %v", cookie, mid, err)
	}

	// DB 读取失败 → 不静默降级，直接报错（避免掩盖数据库故障）
	broken := newFakeStore()
	broken.activeErr = errors.New("db down")
	svc4 := newTestService(t, broken, &fakeVerifier{}, envCookie)
	if _, err := svc4.Resolve(context.Background()); err == nil || !strings.Contains(err.Error(), "读取活跃账号失败") {
		t.Errorf("DB 错误应透出：%v", err)
	}

	// 落库 cookie 损坏 → 继续走环境兜底
	corrupt := newFakeStore(Account{ID: "account-bad", MID: "7", Cookie: "", Status: StatusActive, IsActive: true})
	svc5 := newTestService(t, corrupt, &fakeVerifier{}, envCookie)
	cred, err = svc5.Resolve(context.Background())
	if err != nil || cred.Source != SourceEnv {
		t.Errorf("坏记录应回退环境凭据：%+v %v", cred, err)
	}
}

// TestEnsureValidation Ensure 校验 MID/cookie/状态并生成稳定 id。
func TestEnsureValidation(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, &fakeVerifier{}, "")

	if _, err := svc.Ensure(context.Background(), Account{MID: "abc", Cookie: dbCookie}); !errors.Is(err, ErrVerify) {
		t.Errorf("非法 UID 应拒绝：%v", err)
	}
	if _, err := svc.Ensure(context.Background(), Account{MID: "42", Cookie: "no-sessdata"}); !errors.Is(err, ErrInvalidCookie) {
		t.Errorf("非法 cookie 应拒绝：%v", err)
	}
	if _, err := svc.Ensure(context.Background(), Account{MID: "42", Cookie: dbCookie, Status: "weird"}); err == nil {
		t.Error("非法状态应拒绝")
	}

	got, err := svc.Ensure(context.Background(), Account{MID: " 42 ", Cookie: " " + dbCookie + " ", Note: strings.Repeat("鱼", 130)})
	if err != nil {
		t.Fatalf("Ensure 失败：%v", err)
	}
	if got.ID != IDForMID("42") || got.Name != "UID 42" || got.CSRF != "csrf-db" || got.Status != StatusActive {
		t.Errorf("默认值不符：%+v", got)
	}
	if !got.IsActive {
		t.Error("首个账号应自动活跃")
	}
	if len([]rune(got.Note)) != 121 || !strings.HasSuffix(got.Note, "…") {
		t.Errorf("note 应截断到 120 字：%q", got.Note)
	}
}

// TestAddUsesVerifier Add 先验证身份再落库。
func TestAddUsesVerifier(t *testing.T) {
	verify := &fakeVerifier{info: Identity{MID: "123", Name: "小鱼"}}
	store := newFakeStore()
	svc := newTestService(t, store, verify, "")

	got, err := svc.Add(context.Background(), " "+dbCookie+" ", "常用")
	if err != nil {
		t.Fatalf("Add 失败：%v", err)
	}
	if len(verify.calls) != 1 || verify.calls[0] != dbCookie {
		t.Errorf("验证入参未规范化：%v", verify.calls)
	}
	if got.MID != "123" || got.Name != "小鱼" || got.Note != "常用" || got.Status != StatusActive {
		t.Errorf("落库内容不符：%+v", got)
	}
	if got.LastCheck.IsZero() || !got.IsActive {
		t.Errorf("LastCheck/活跃状态不符：%+v", got)
	}

	verify.err = bili.ErrNotLoggedIn
	if _, err := svc.Add(context.Background(), dbCookie, ""); !errors.Is(err, ErrVerify) {
		t.Errorf("验证失败应包装 ErrVerify：%v", err)
	}
	verify.err = nil
	verify.info = Identity{MID: "bad"}
	if _, err := svc.Add(context.Background(), dbCookie, ""); !errors.Is(err, ErrVerify) {
		t.Errorf("无效 UID 应拒绝：%v", err)
	}
}

// TestActivateAndDelete 切换活跃；活跃账号禁止删除。
func TestActivateAndDelete(t *testing.T) {
	first := Account{ID: "account-1", MID: "1", Name: "一号", Cookie: dbCookie, Status: StatusActive, IsActive: true}
	second := Account{ID: "account-2", MID: "2", Name: "二号", Cookie: dbCookie, Status: StatusActive}
	store := newFakeStore(first, second)
	// 切换必须用 Nav 身份确认目标账号：验证器需返回目标 MID。
	svc := newTestService(t, store, &fakeVerifier{info: Identity{MID: "2", Name: "二号"}}, "")

	if err := svc.Activate(context.Background(), "account-2"); err != nil {
		t.Fatalf("切换失败：%v", err)
	}
	active, ok, err := svc.Active(context.Background())
	if err != nil || !ok || active.ID != "account-2" {
		t.Fatalf("活跃账号不符：%+v %v %v", active, ok, err)
	}
	if err := svc.Delete(context.Background(), "account-2"); !errors.Is(err, ErrActiveAccount) {
		t.Errorf("活跃账号应禁止删除：%v", err)
	}
	if err := svc.Delete(context.Background(), "account-1"); err != nil {
		t.Errorf("非活跃账号应可删除：%v", err)
	}
	if err := svc.Delete(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在应返回 ErrNotFound：%v", err)
	}
	if err := svc.Activate(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("切换不存在账号应 ErrNotFound：%v", err)
	}
}

// blockingVerifier 在 Verify 内阻塞，用于观察切换屏障的时序。
type blockingVerifier struct {
	info    Identity
	err     error
	started chan struct{}
	release chan struct{}
}

func (b *blockingVerifier) Verify(ctx context.Context, _ string) (Identity, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return b.info, b.err
	case <-ctx.Done():
		return Identity{}, ctx.Err()
	}
}

func newBlockingVerifier(info Identity) *blockingVerifier {
	return &blockingVerifier{info: info, started: make(chan struct{}, 1), release: make(chan struct{})}
}

func waitSwitching(t *testing.T, gate *Gate) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		gate.mu.Lock()
		switching := gate.switching
		gate.mu.Unlock()
		if switching {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("账号切换未进入独占窗口")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestActivateWaitsForInflightAndBlocksAdmission 切换先等在途操作结清，期间新准入被阻塞。
func TestActivateWaitsForInflightAndBlocksAdmission(t *testing.T) {
	first := Account{ID: "account-1", MID: "1", Name: "一号", Cookie: dbCookie, Status: StatusActive, IsActive: true}
	second := Account{ID: "account-2", MID: "2", Name: "二号", Cookie: dbCookie, Status: StatusActive}
	store := newFakeStore(first, second)
	verify := newBlockingVerifier(Identity{MID: "2", Name: "二号"})
	svc := newTestService(t, store, verify, "")

	releaseFirst, err := svc.BeginOperation(context.Background())
	if err != nil {
		t.Fatalf("旧操作未取得准入：%v", err)
	}

	activateDone := make(chan error, 1)
	go func() { activateDone <- svc.Activate(context.Background(), "account-2") }()
	waitSwitching(t, svc.gate)

	blocked := make(chan error, 1)
	go func() {
		release, err := svc.BeginOperation(context.Background())
		if release != nil {
			release()
		}
		blocked <- err
	}()
	select {
	case <-blocked:
		t.Fatal("旧操作未结清时新准入被放行")
	case <-time.After(50 * time.Millisecond):
	}

	releaseFirst()
	select {
	case <-verify.started:
	case <-time.After(time.Second):
		t.Fatal("切换未进入身份验证阶段")
	}
	// 先验证后提交：Verify 阻塞期间活跃账号必须仍是旧号。
	active, ok, err := svc.Active(context.Background())
	if err != nil || !ok || active.ID != "account-1" {
		t.Fatalf("验证期间活跃账号被提前改写：%+v %v %v", active, ok, err)
	}
	select {
	case <-blocked:
		t.Fatal("验证未结束就放行了新准入")
	case <-time.After(50 * time.Millisecond):
	}

	close(verify.release)
	if err := <-activateDone; err != nil {
		t.Fatalf("切换失败：%v", err)
	}
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("切换结束后准入仍失败：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("切换结束后新准入未被唤醒")
	}
	active, ok, err = svc.Active(context.Background())
	if err != nil || !ok || active.ID != "account-2" {
		t.Fatalf("活跃账号不符：%+v %v %v", active, ok, err)
	}
}

// TestActivateRejectsConcurrentSwitch 同一时刻只允许一次切换。
func TestActivateRejectsConcurrentSwitch(t *testing.T) {
	first := Account{ID: "account-1", MID: "1", Name: "一号", Cookie: dbCookie, Status: StatusActive, IsActive: true}
	second := Account{ID: "account-2", MID: "2", Name: "二号", Cookie: dbCookie, Status: StatusActive}
	store := newFakeStore(first, second)
	verify := newBlockingVerifier(Identity{MID: "2", Name: "二号"})
	svc := newTestService(t, store, verify, "")

	firstDone := make(chan error, 1)
	go func() { firstDone <- svc.Activate(context.Background(), "account-2") }()
	select {
	case <-verify.started:
	case <-time.After(time.Second):
		t.Fatal("首次切换未开始验证")
	}
	if err := svc.Activate(context.Background(), "account-1"); !errors.Is(err, ErrSwitchInProgress) {
		t.Fatalf("并发切换应返回 ErrSwitchInProgress：%v", err)
	}
	close(verify.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("首次切换失败：%v", err)
	}
}

// TestActivateVerifyFailureKeepsActive 身份不一致/验证失败时活跃账号保持不变。
// 先验证后提交：失败路径从未写过活跃标志，因此不需要回滚，也不存在回滚失败。
func TestActivateVerifyFailureKeepsActive(t *testing.T) {
	t.Run("mismatch", func(t *testing.T) {
		first := Account{ID: "account-1", MID: "1", Name: "一号", Cookie: dbCookie, Status: StatusActive, IsActive: true}
		second := Account{ID: "account-2", MID: "2", Name: "二号", Cookie: dbCookie, Status: StatusActive}
		store := newFakeStore(first, second)
		store.strictContext = true
		svc := newTestService(t, store, &fakeVerifier{info: Identity{MID: "99", Name: "别人"}}, "")

		if err := svc.Activate(context.Background(), "account-2"); !errors.Is(err, ErrVerify) {
			t.Fatalf("UID 不一致应 ErrVerify：%v", err)
		}
		active, ok, err := svc.Active(context.Background())
		if err != nil || !ok || active.ID != "account-1" {
			t.Fatalf("活跃账号应保持原样：%+v %v %v", active, ok, err)
		}
		if store.accounts["account-2"].IsActive {
			t.Errorf("验证失败的目标不应成为活跃账号：%+v", store.accounts["account-2"])
		}
		if store.accounts["account-2"].Status != StatusActive {
			t.Errorf("非认证错误不应标记 invalid：%+v", store.accounts["account-2"])
		}
		release, err := svc.BeginOperation(context.Background())
		if err != nil {
			t.Fatalf("切换失败后准入未恢复：%v", err)
		}
		release()
	})
	t.Run("auth_failure", func(t *testing.T) {
		first := Account{ID: "account-1", MID: "1", Name: "一号", Cookie: dbCookie, Status: StatusActive, IsActive: true}
		second := Account{ID: "account-2", MID: "2", Name: "二号", Cookie: dbCookie, Status: StatusActive}
		store := newFakeStore(first, second)
		store.strictContext = true
		svc := newTestService(t, store, &fakeVerifier{err: bili.ErrNotLoggedIn}, "")

		if err := svc.Activate(context.Background(), "account-2"); !errors.Is(err, ErrVerify) {
			t.Fatalf("认证失败应 ErrVerify：%v", err)
		}
		active, ok, _ := svc.Active(context.Background())
		if !ok || active.ID != "account-1" {
			t.Fatalf("活跃账号应保持原样：%+v %v", active, ok)
		}
		if store.accounts["account-2"].Status != StatusInvalid {
			t.Errorf("认证失败应标记 invalid：%+v", store.accounts["account-2"])
		}
	})
	t.Run("no_previous_keeps_none", func(t *testing.T) {
		target := Account{ID: "account-2", MID: "2", Name: "二号", Cookie: dbCookie, Status: StatusActive}
		store := newFakeStore(target)
		store.strictContext = true
		svc := newTestService(t, store, &fakeVerifier{err: bili.ErrNotLoggedIn}, "")

		if err := svc.Activate(context.Background(), "account-2"); !errors.Is(err, ErrVerify) {
			t.Fatalf("认证失败应 ErrVerify：%v", err)
		}
		active, ok, err := svc.Active(context.Background())
		if err != nil || ok || active.ID != "" {
			t.Fatalf("原本没有活跃账号时应保持没有：%+v %v %v", active, ok, err)
		}
		if store.accounts["account-2"].Status != StatusInvalid {
			t.Errorf("认证失败应标记 invalid：%+v", store.accounts["account-2"])
		}
	})
}

// deadlineVerifier 一直等到 ctx 截止才返回，用于覆盖 Verify 耗尽切换预算的路径。
type deadlineVerifier struct {
	result  error
	entered chan struct{}
}

func newDeadlineVerifier(result error) *deadlineVerifier {
	return &deadlineVerifier{result: result, entered: make(chan struct{}, 1)}
}

func (d *deadlineVerifier) Verify(ctx context.Context, _ string) (Identity, error) {
	select {
	case d.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	if d.result != nil {
		return Identity{}, d.result
	}
	return Identity{}, ctx.Err()
}

// TestActivateVerifyDeadlineKeepsOldActive Verify 等到 context 截止时：
// 切换报错、旧活跃账号保持、失败记账仍落库、准入恢复后解析到旧账号。
func TestActivateVerifyDeadlineKeepsOldActive(t *testing.T) {
	first := Account{ID: "account-1", MID: "1", Name: "一号", Cookie: dbCookie, Status: StatusActive, IsActive: true}
	second := Account{ID: "account-2", MID: "2", Name: "二号", Cookie: dbCookie, Status: StatusActive}
	store := newFakeStore(first, second)
	store.strictContext = true
	verify := newDeadlineVerifier(nil)
	svc := newTestService(t, store, verify, "")
	svc.switchTimeout = 120 * time.Millisecond

	started := time.Now()
	err := svc.Activate(context.Background(), "account-2")
	elapsed := time.Since(started)

	if !errors.Is(err, ErrVerify) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Verify 耗尽截止应返回 ErrVerify+DeadlineExceeded：%v", err)
	}
	if elapsed < svc.switchTimeout {
		t.Fatalf("Verify 必须等到 context 截止才返回（实际 %v）", elapsed)
	}
	select {
	case <-verify.entered:
	default:
		t.Fatal("Verify 未被调用")
	}
	active, ok, activeErr := svc.Active(context.Background())
	if activeErr != nil || !ok || active.ID != "account-1" {
		t.Fatalf("活跃账号应保持旧号：%+v %v %v", active, ok, activeErr)
	}
	if store.accounts["account-2"].IsActive {
		t.Errorf("超时的目标不应成为活跃账号：%+v", store.accounts["account-2"])
	}
	if len(store.expiredCalls) != 0 {
		t.Errorf("记账不应复用已过期 ctx：%v", store.expiredCalls)
	}
	if len(store.updates) != 1 {
		t.Fatalf("失败记账应使用独立 cleanup ctx 落库：%+v", store.updates)
	}
	if last := store.updates[0]; last.id != "account-2" || last.status != StatusActive {
		t.Errorf("超时不是认证错误，状态应保持原样：%+v", last)
	}
	release, err := svc.BeginOperation(context.Background())
	if err != nil {
		t.Fatalf("切换结束后准入未恢复：%v", err)
	}
	release()
	cred, err := svc.Resolve(context.Background())
	if err != nil || cred.AccountID != "account-1" {
		t.Fatalf("准入恢复后凭据应仍是旧账号：%+v %v", cred, err)
	}
}

// TestActivateVerifyDeadlineMarksInvalid Verify 等到截止后才报认证失败：
// 状态标记 invalid，活跃账号仍保持不变。
func TestActivateVerifyDeadlineMarksInvalid(t *testing.T) {
	first := Account{ID: "account-1", MID: "1", Name: "一号", Cookie: dbCookie, Status: StatusActive, IsActive: true}
	second := Account{ID: "account-2", MID: "2", Name: "二号", Cookie: dbCookie, Status: StatusActive}
	store := newFakeStore(first, second)
	store.strictContext = true
	verify := newDeadlineVerifier(bili.ErrNotLoggedIn)
	svc := newTestService(t, store, verify, "")
	svc.switchTimeout = 120 * time.Millisecond

	err := svc.Activate(context.Background(), "account-2")
	if !errors.Is(err, ErrVerify) || !errors.Is(err, bili.ErrNotLoggedIn) {
		t.Fatalf("应返回 ErrVerify+认证错误：%v", err)
	}
	active, ok, activeErr := svc.Active(context.Background())
	if activeErr != nil || !ok || active.ID != "account-1" {
		t.Fatalf("活跃账号应保持旧号：%+v %v %v", active, ok, activeErr)
	}
	if store.accounts["account-2"].Status != StatusInvalid {
		t.Errorf("认证失败应在截止后仍被记账为 invalid：%+v", store.accounts["account-2"])
	}
	if store.accounts["account-2"].IsActive {
		t.Errorf("超时的目标不应成为活跃账号：%+v", store.accounts["account-2"])
	}
	if len(store.expiredCalls) != 0 {
		t.Errorf("记账不应复用已过期 ctx：%v", store.expiredCalls)
	}
}

// TestActorByMIDStaysOnOldAccount 旧尝试按平台 UID 取凭据，绝不回落到当前活跃账号。
func TestActorByMIDStaysOnOldAccount(t *testing.T) {
	old := Account{ID: "account-1", MID: "1", Name: "一号", Cookie: dbCookie, CSRF: "csrf-db", Status: StatusActive}
	active := Account{ID: "account-2", MID: "2", Name: "二号", Cookie: dbCookie, Status: StatusActive, IsActive: true}
	disabled := Account{ID: "account-3", MID: "3", Name: "三号", Cookie: dbCookie, Status: StatusDisabled}
	store := newFakeStore(old, active, disabled)
	svc := newTestService(t, store, &fakeVerifier{}, "")

	actor, err := svc.ActorByMID(context.Background(), "1")
	if err != nil {
		t.Fatalf("旧账号快照失败：%v", err)
	}
	if actor.AccountID != "account-1" || actor.MID != "1" || actor.Cookie != dbCookie || actor.CSRF != "csrf-db" || actor.Source != SourceDB {
		t.Fatalf("旧账号快照不符：%+v", actor)
	}
	if _, err := svc.ActorByMID(context.Background(), "999"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("未知 MID 不应回落当前活跃账号：%v", err)
	}
	if _, err := svc.ActorByMID(context.Background(), "3"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("停用账号不应提供凭据：%v", err)
	}
	current, err := svc.CurrentActor(context.Background())
	if err != nil || current.AccountID != "account-2" || current.MID != "2" {
		t.Fatalf("当前账号快照不符：%+v %v", current, err)
	}
}

// TestActorByMIDEnvFallback 环境变量凭据按配置的 EnvMID 可被旧尝试找回。
func TestActorByMIDEnvFallback(t *testing.T) {
	svc, err := NewService(Options{
		Store:     newFakeStore(),
		Verifier:  &fakeVerifier{},
		EnvCookie: envCookie,
		EnvMID:    "77",
	})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := svc.ActorByMID(context.Background(), "77")
	if err != nil {
		t.Fatalf("环境凭据未命中：%v", err)
	}
	if actor.AccountID != "env-77" || actor.MID != "77" || actor.Cookie != envCookie || actor.Source != SourceEnv {
		t.Fatalf("环境账号快照不符：%+v", actor)
	}
	if _, err := svc.ActorByMID(context.Background(), "88"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("未配置的 MID 不应命中：%v", err)
	}
	current, err := svc.CurrentActor(context.Background())
	if err != nil || current.MID != "77" || current.Source != SourceEnv {
		t.Fatalf("环境当前账号快照不符：%+v %v", current, err)
	}

	empty, err := NewService(Options{Store: newFakeStore(), Verifier: &fakeVerifier{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := empty.CurrentActor(context.Background()); !errors.Is(err, bili.ErrNoCredential) {
		t.Fatalf("无凭据应映射 bili.ErrNoCredential：%v", err)
	}
}

// TestCheckMarksInvalid 认证失败标记 invalid；网络错误保留状态。
func TestCheckMarksInvalid(t *testing.T) {
	stored := Account{ID: "account-1", MID: "42", Name: "旧名", Cookie: dbCookie, Status: StatusActive, IsActive: true}
	verify := &fakeVerifier{}
	store := newFakeStore(stored)
	svc := newTestService(t, store, verify, "")

	verify.info = Identity{MID: "42", Name: "新名"}
	got, err := svc.Check(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("Check 失败：%v", err)
	}
	if got.Name != "新名" || got.Status != StatusActive || !got.LastCheck.Equal(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("验证成功未更新：%+v", got)
	}

	verify.err = bili.ErrNotLoggedIn
	got, err = svc.Check(context.Background(), "account-1")
	if !errors.Is(err, ErrVerify) {
		t.Fatalf("失效应返回 ErrVerify：%v", err)
	}
	if got.Status != StatusInvalid || store.accounts["account-1"].Status != StatusInvalid {
		t.Errorf("认证失败应标记 invalid：%+v", got)
	}
	last := store.updates[len(store.updates)-1]
	if last.status != StatusInvalid || !last.at.Equal(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("UpdateCheck 入参不符：%+v", last)
	}

	// 网络错误：只刷新 last_check，不改状态
	store.accounts["account-1"] = Account{ID: "account-1", MID: "42", Name: "旧名", Cookie: dbCookie, Status: StatusActive, IsActive: true}
	verify.err = errors.New("网络请求失败")
	got, err = svc.Check(context.Background(), "account-1")
	if !errors.Is(err, ErrVerify) || got.Status != StatusActive {
		t.Errorf("网络错误不应标 invalid：%+v %v", got, err)
	}

	// UID 不一致：视为验证失败，不改状态
	verify.err = nil
	verify.info = Identity{MID: "99", Name: "别人"}
	got, err = svc.Check(context.Background(), "account-1")
	if !errors.Is(err, ErrVerify) || got.Status != StatusActive {
		t.Errorf("UID 不一致应拒绝：%+v %v", got, err)
	}
}

// TestMaskCookie 只返回字段名与掩码，绝不含原值。
func TestMaskCookie(t *testing.T) {
	got := MaskCookie("SESSDATA=secret-1; bili_jct=secret-2; DedeUserID=9; buvid3=secret-3")
	if strings.Contains(got, "secret") || !strings.Contains(got, "SESSDATA=***") ||
		!strings.Contains(got, "bili_jct=***") || !strings.Contains(got, "buvid3=***") {
		t.Errorf("脱敏结果不符：%q", got)
	}
	if strings.Contains(got, "DedeUserID") {
		t.Errorf("不应出现非白名单字段：%q", got)
	}
	if MaskCookie("") != "已保存" {
		t.Errorf("空凭据应显示已保存：%q", MaskCookie(""))
	}
}

// TestIDForMID 稳定且按 mid 去空格。
func TestIDForMID(t *testing.T) {
	if IDForMID("42") != IDForMID(" 42 ") {
		t.Error("id 应对 mid 去空格后稳定")
	}
	if IDForMID("42") == IDForMID("43") {
		t.Error("不同 mid 不应碰撞")
	}
	if !strings.HasPrefix(IDForMID("42"), "account-") {
		t.Errorf("id 前缀不符：%s", IDForMID("42"))
	}
}
