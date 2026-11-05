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

func (f *fakeStore) SetActive(_ context.Context, id string) error {
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

func (f *fakeStore) Delete(_ context.Context, id string) error {
	if _, ok := f.accounts[id]; !ok {
		return ErrNotFound
	}
	delete(f.accounts, id)
	return nil
}

func (f *fakeStore) UpdateCheck(_ context.Context, id, name, status string, at time.Time) error {
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
	svc := newTestService(t, store, &fakeVerifier{}, "")

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
