// Package account 管理 B站账号：多账号存储、唯一活跃账号与凭据解析链。
//
// 边界：
//   - DB 里的 cookie 是本地明文（个人项目，见 docs/12）；日志与 API 只出现脱敏值；
//   - 账号验证通过 bili get_info（nav）完成，失效只标记状态，不影响主链路启动；
//   - 凭据解析链固定为「DB 活跃账号优先 → 环境变量兜底」，保持既有部署兼容。
package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/bili"
)

// 账号状态：active=验证可用，invalid=验证失效，disabled=人工停用。
const (
	StatusActive   = "active"
	StatusInvalid  = "invalid"
	StatusDisabled = "disabled"
)

// 凭据来源，用于日志与诊断（不含凭据本身）。
const (
	SourceDB  = "db"
	SourceEnv = "env"
)

var (
	// ErrNotFound 表示账号不存在。
	ErrNotFound = errors.New("账号不存在")
	// ErrNoCredential 表示 DB 与环境都没有可用凭据。
	ErrNoCredential = errors.New("没有可用的B站凭据")
	// ErrInvalidCookie 表示 cookie 格式不合法。
	ErrInvalidCookie = errors.New("B站凭据格式无效")
	// ErrVerify 表示调用 B站接口验证账号失败。
	ErrVerify = errors.New("B站账号验证失败")
	// ErrActiveAccount 表示当前活跃账号不允许直接删除。
	ErrActiveAccount = errors.New("不能删除当前活跃账号")
)

// Account 是一个 B站账号；Cookie/CSRF 只在进程内部使用，不直接序列化。
type Account struct {
	ID        string
	MID       string
	Name      string
	Cookie    string
	CSRF      string
	Status    string
	LastCheck time.Time
	Note      string
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Identity 是 B站返回的账号身份。
type Identity struct {
	MID  string
	Name string
}

// Credential 是解析链输出的一份可用凭据。
type Credential struct {
	Cookie    string
	CSRF      string
	MID       string
	AccountID string
	Source    string
}

// Store 是账号持久化接口，由 database 层实现。
type Store interface {
	List(ctx context.Context) ([]Account, error)
	Get(ctx context.Context, id string) (Account, error)
	// Active 在没有活跃账号时返回 ErrNotFound。
	Active(ctx context.Context) (Account, error)
	// Upsert 按 mid 幂等写入；当前没有活跃账号时自动把该账号设为活跃。
	Upsert(ctx context.Context, a Account) (Account, error)
	SetActive(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	UpdateCheck(ctx context.Context, id, name, status string, at time.Time) error
}

// Verifier 调用 B站接口确认 cookie 对应的账号身份。
type Verifier interface {
	Verify(ctx context.Context, cookie string) (Identity, error)
}

// Options 是 Service 依赖；EnvCookie/EnvCookieFile 是解析链兜底。
type Options struct {
	Store         Store
	Verifier      Verifier
	EnvCookie     string
	EnvCookieFile string
	Now           func() time.Time
}

// Service 提供账号 CRUD、验证与凭据解析。
type Service struct {
	store         Store
	verifier      Verifier
	envCookie     string
	envCookieFile string
	now           func() time.Time
}

// NewService 装配账号服务；Store 与 Verifier 缺一不可。
func NewService(o Options) (*Service, error) {
	if o.Store == nil || o.Verifier == nil {
		return nil, errors.New("账号服务依赖不能为空")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:         o.Store,
		verifier:      o.Verifier,
		envCookie:     strings.TrimSpace(o.EnvCookie),
		envCookieFile: strings.TrimSpace(o.EnvCookieFile),
		now:           now,
	}, nil
}

// Load 列出全部账号（活跃在前）。
func (s *Service) Load(ctx context.Context) ([]Account, error) {
	if s == nil {
		return nil, errors.New("账号服务不能为空")
	}
	return s.store.List(ctx)
}

// Active 返回当前活跃账号；不存在时第二返回值为 false。
func (s *Service) Active(ctx context.Context) (Account, bool, error) {
	if s == nil {
		return Account{}, false, errors.New("账号服务不能为空")
	}
	stored, err := s.store.Active(ctx)
	if errors.Is(err, ErrNotFound) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	return stored, true, nil
}

// Ensure 幂等写入账号；cookie 必须合法，MID 必须为纯数字。
func (s *Service) Ensure(ctx context.Context, a Account) (Account, error) {
	if s == nil {
		return Account{}, errors.New("账号服务不能为空")
	}
	mid := strings.TrimSpace(a.MID)
	if !digitsOnly(mid) {
		return Account{}, fmt.Errorf("%w：B站UID无效", ErrVerify)
	}
	cookie, err := normalizeCookie(a.Cookie)
	if err != nil {
		return Account{}, err
	}
	status := strings.TrimSpace(a.Status)
	if status == "" {
		status = StatusActive
	}
	if status != StatusActive && status != StatusInvalid && status != StatusDisabled {
		return Account{}, errors.New("账号状态无效")
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = "UID " + mid
	}
	csrf := strings.TrimSpace(a.CSRF)
	if csrf == "" {
		csrf = bili.CSRFToken(cookie)
	}
	id := strings.TrimSpace(a.ID)
	if id == "" {
		id = IDForMID(mid)
	}
	return s.store.Upsert(ctx, Account{
		ID:        id,
		MID:       mid,
		Name:      name,
		Cookie:    cookie,
		CSRF:      csrf,
		Status:    status,
		LastCheck: a.LastCheck,
		Note:      clipRunes(strings.TrimSpace(a.Note), 120),
	})
}

// Validate 只做 cookie 校验与 B站身份确认，不落库。
func (s *Service) Validate(ctx context.Context, cookie string) (Identity, error) {
	if s == nil {
		return Identity{}, errors.New("账号服务不能为空")
	}
	normalized, err := normalizeCookie(cookie)
	if err != nil {
		return Identity{}, err
	}
	identity, err := s.verifier.Verify(ctx, normalized)
	if err != nil {
		return Identity{}, fmt.Errorf("%w：%w", ErrVerify, err)
	}
	identity.MID = strings.TrimSpace(identity.MID)
	identity.Name = strings.TrimSpace(identity.Name)
	if !digitsOnly(identity.MID) {
		return Identity{}, fmt.Errorf("%w：B站未返回有效UID", ErrVerify)
	}
	if identity.Name == "" {
		identity.Name = "UID " + identity.MID
	}
	return identity, nil
}

// Add 验证并保存账号；当前没有活跃账号时自动激活。
func (s *Service) Add(ctx context.Context, cookie, note string) (Account, error) {
	identity, err := s.Validate(ctx, cookie)
	if err != nil {
		return Account{}, err
	}
	normalized, _ := normalizeCookie(cookie)
	return s.Ensure(ctx, Account{
		ID:        IDForMID(identity.MID),
		MID:       identity.MID,
		Name:      identity.Name,
		Cookie:    normalized,
		CSRF:      bili.CSRFToken(normalized),
		Status:    StatusActive,
		LastCheck: s.now().UTC(),
		Note:      note,
	})
}

// Activate 切换活跃账号。
func (s *Service) Activate(ctx context.Context, id string) error {
	if s == nil {
		return errors.New("账号服务不能为空")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrNotFound
	}
	return s.store.SetActive(ctx, id)
}

// Delete 删除非活跃账号；活跃账号必须先切换，避免误删当前凭据。
func (s *Service) Delete(ctx context.Context, id string) error {
	if s == nil {
		return errors.New("账号服务不能为空")
	}
	stored, err := s.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if stored.IsActive {
		return ErrActiveAccount
	}
	return s.store.Delete(ctx, stored.ID)
}

// Check 重新验证账号：成功更新昵称与状态，认证失败标记 invalid。
func (s *Service) Check(ctx context.Context, id string) (Account, error) {
	if s == nil {
		return Account{}, errors.New("账号服务不能为空")
	}
	stored, err := s.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return Account{}, err
	}
	now := s.now().UTC()
	identity, verifyErr := s.verifier.Verify(ctx, stored.Cookie)
	if verifyErr == nil {
		identity.MID = strings.TrimSpace(identity.MID)
		identity.Name = strings.TrimSpace(identity.Name)
		if !digitsOnly(identity.MID) || identity.MID != stored.MID {
			verifyErr = fmt.Errorf("%w：B站返回的UID与账号不一致", ErrVerify)
		} else {
			if identity.Name == "" {
				identity.Name = stored.Name
			}
			if err := s.store.UpdateCheck(ctx, stored.ID, identity.Name, StatusActive, now); err != nil {
				return Account{}, err
			}
			stored.Name = identity.Name
			stored.Status = StatusActive
			stored.LastCheck = now
			return stored, nil
		}
	}
	status := stored.Status
	if bili.IsAuthError(verifyErr) {
		status = StatusInvalid
	}
	if err := s.store.UpdateCheck(ctx, stored.ID, stored.Name, status, now); err != nil {
		return Account{}, err
	}
	stored.Status = status
	stored.LastCheck = now
	return stored, fmt.Errorf("%w：%w", ErrVerify, verifyErr)
}

// Resolve 按「DB 活跃账号 → 环境变量」返回当前凭据。
func (s *Service) Resolve(ctx context.Context) (Credential, error) {
	if s == nil {
		return Credential{}, ErrNoCredential
	}
	stored, err := s.store.Active(ctx)
	switch {
	case err == nil:
		cookie, cookieErr := bili.LoadCookie(stored.Cookie, "")
		if cookieErr == nil {
			csrf := stored.CSRF
			if csrf == "" {
				csrf = bili.CSRFToken(cookie)
			}
			return Credential{Cookie: cookie, CSRF: csrf, MID: stored.MID, AccountID: stored.ID, Source: SourceDB}, nil
		}
		// 落库值损坏时继续走环境兜底，避免一条坏记录拖垮整个进程。
	case errors.Is(err, ErrNotFound):
	default:
		return Credential{}, fmt.Errorf("读取活跃账号失败：%w", err)
	}
	cookie, err := bili.LoadCookie(s.envCookie, s.envCookieFile)
	if errors.Is(err, bili.ErrNoCredential) {
		return Credential{}, ErrNoCredential
	}
	if err != nil {
		return Credential{}, fmt.Errorf("环境变量B站凭据不可用：%w", ErrInvalidCookie)
	}
	return Credential{Cookie: cookie, CSRF: bili.CSRFToken(cookie), Source: SourceEnv}, nil
}

// BiliCredential 适配 bili.Options.Resolver；无凭据时返回 bili.ErrNoCredential。
func (s *Service) BiliCredential(ctx context.Context) (cookie, selfMID string, err error) {
	credential, err := s.Resolve(ctx)
	if errors.Is(err, ErrNoCredential) {
		return "", "", bili.ErrNoCredential
	}
	if err != nil {
		return "", "", err
	}
	return credential.Cookie, credential.MID, nil
}

// IDForMID 按 UID 生成稳定内部 id。
func IDForMID(mid string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(mid)))
	return "account-" + hex.EncodeToString(sum[:6])
}

// MaskCookie 只返回凭据字段名与固定掩码，绝不包含原值。
func MaskCookie(cookie string) string {
	wanted := []string{"SESSDATA", "bili_jct", "buvid3"}
	present := make(map[string]bool, len(wanted))
	for _, part := range strings.Split(cookie, ";") {
		key := strings.TrimSpace(part)
		if i := strings.IndexByte(key, '='); i >= 0 {
			key = strings.TrimSpace(key[:i])
		}
		present[key] = true
	}
	masked := make([]string, 0, len(wanted))
	for _, key := range wanted {
		if present[key] {
			masked = append(masked, key+"=***")
		}
	}
	if len(masked) == 0 {
		return "已保存"
	}
	return strings.Join(masked, "; ")
}

func normalizeCookie(cookie string) (string, error) {
	value, err := bili.LoadCookie(strings.TrimSpace(cookie), "")
	if err != nil {
		return "", fmt.Errorf("%w：%v", ErrInvalidCookie, err)
	}
	return value, nil
}

func digitsOnly(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func clipRunes(value string, max int) string {
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return value
	}
	return string([]rune(value)[:max]) + "…"
}
