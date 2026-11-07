package dm

// 本文件把「当日收信人」持久化为 actor/day 预占：`state/dm-recipient.json`。
//
// 语义：
//   - 键 = 发送账号（B站 MID）+ UTC 日期；每个账号每天最多 1 个收信人；
//   - 预占在 POST 前原子写入：进程内 mutex + O_EXCL 锁文件 + 临时文件 rename；
//   - 明确未发出（白名单/限频/互关/鉴权拒绝）才释放；结果未知或重试保留，
//     宁可少发给另一个收信人，也不能绕过每日限制；
//   - 发送成功记 committed，之后并发的失败尝试不能再释放当天名额；
//   - 跨天自动重置；旧格式 {date, peer} 继续可读（视为无 actor 的单账号记录）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	recipientLockWait  = 2 * time.Second
	recipientLockStale = 30 * time.Second
	recipientLockPoll  = 10 * time.Millisecond
)

// errRecipientLocked 表示同一 actor 当天已锁定给另一个收信人。
var errRecipientLocked = errors.New("今天已锁定收信人")

// recipientState 是落盘结构。
type recipientState struct {
	Version int `json:"version,omitempty"`
	// Date 是 UTC 日期（YYYY-MM-DD）。
	Date string `json:"date"`
	// Peer 是旧格式/无 actor 场景下的收信人，保留以兼容既有状态文件。
	Peer string `json:"peer,omitempty"`
	// Peers 是 actor(MID) → 当天收信人。
	Peers map[string]string `json:"peers,omitempty"`
	// Committed 是已成功发送的 actor → 收信人；成功后不允许再释放当天名额。
	Committed map[string]string `json:"committed,omitempty"`
}

// RecipientStore 是当日收信人的持久化存储。
type RecipientStore struct {
	path string
	mu   sync.Mutex
}

// NewRecipientStore 只记录路径，不创建文件。
func NewRecipientStore(path string) (*RecipientStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, &Error{Kind: KindInput, Cause: errors.New("收信人状态文件路径不能为空")}
	}
	if strings.ContainsAny(path, "\x00\r\n") {
		return nil, &Error{Kind: KindInput, Cause: errors.New("收信人状态文件路径非法")}
	}
	return &RecipientStore{path: path}, nil
}

// Load 读取状态；文件不存在返回零值，坏 JSON 按「没有记录」降级。
func (s *RecipientStore) Load() (recipientState, error) {
	if s == nil || s.path == "" {
		return recipientState{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *RecipientStore) loadLocked() (recipientState, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return recipientState{}, nil
	}
	if err != nil {
		return recipientState{}, fmt.Errorf("读取收信人状态：%w", err)
	}
	var st recipientState
	if err := json.Unmarshal(data, &st); err != nil {
		// 坏文件当作「没有记录」：宁可放过一天，也不要永久卡死发送
		return recipientState{}, nil //nolint:nilerr // 有意降级：坏状态不该挡住发送
	}
	return normalizeRecipient(st), nil
}

// Reserve 原子预占当天收信人：同一 actor 换人立即拒绝，同人放行。
//
// 预占必须发生在 POST 之前；写入失败返回错误，调用方不得继续发送。
func (s *RecipientStore) Reserve(day, actor, peer string) error {
	day, actor, peer = strings.TrimSpace(day), strings.TrimSpace(actor), strings.TrimSpace(peer)
	if s == nil || s.path == "" {
		return errors.New("收信人状态存储未初始化")
	}
	if day == "" || peer == "" {
		return errors.New("收信人预占参数无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return fmt.Errorf("收信人预占锁不可用：%w", err)
	}
	defer unlock()

	st, err := s.loadLocked()
	if err != nil {
		return err
	}
	st = st.forDay(day)
	if existing := st.Peers[actor]; existing != "" && existing != peer {
		return &Error{Kind: KindInput, Cause: errRecipientLocked}
	}
	st.Peers[actor] = peer
	if actor == "" {
		st.Peer = peer
	}
	if err := s.saveLocked(st); err != nil {
		return fmt.Errorf("保存收信人预占：%w", err)
	}
	return nil
}

// Commit 在发送成功后锁定当天名额；写盘失败由调用方按「已发但状态不确定」处理。
func (s *RecipientStore) Commit(day, actor, peer string) error {
	day, actor, peer = strings.TrimSpace(day), strings.TrimSpace(actor), strings.TrimSpace(peer)
	if s == nil || s.path == "" {
		return errors.New("收信人状态存储未初始化")
	}
	if day == "" || peer == "" {
		return errors.New("收信人提交参数无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return fmt.Errorf("收信人提交锁不可用：%w", err)
	}
	defer unlock()

	st, err := s.loadLocked()
	if err != nil {
		return err
	}
	if st.Date != day {
		// 跨天后的旧尝试只结算自己，不能把当天状态改回旧日期。
		return nil
	}
	st = st.forDay(day)
	st.Peers[actor] = peer
	if actor == "" {
		st.Peer = peer
	}
	st.Committed[actor] = peer
	if err := s.saveLocked(st); err != nil {
		return fmt.Errorf("保存收信人状态：%w", err)
	}
	return nil
}

// Release 只释放本 actor 仍持有且尚未成功发送的预占。
//
// 写盘失败时保留占用：宁可少发给另一个收信人，也不能绕过每日限制。
func (s *RecipientStore) Release(day, actor, peer string) {
	day, actor, peer = strings.TrimSpace(day), strings.TrimSpace(actor), strings.TrimSpace(peer)
	if s == nil || s.path == "" || day == "" || peer == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return
	}
	defer unlock()

	st, err := s.loadLocked()
	if err != nil || st.Date != day {
		return
	}
	if st.Committed[actor] == peer {
		return
	}
	if st.Peers[actor] != peer {
		return
	}
	delete(st.Peers, actor)
	if actor == "" {
		st.Peer = ""
	}
	_ = s.saveLocked(st)
}

// Save 原子写入：临时文件 → Sync → Rename。
func (s *RecipientStore) Save(st recipientState) error {
	if s == nil || s.path == "" {
		return errors.New("收信人状态存储未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return fmt.Errorf("收信人状态锁不可用：%w", err)
	}
	defer unlock()
	return s.saveLocked(normalizeRecipient(st))
}

func (s *RecipientStore) saveLocked(st recipientState) error {
	st.Version = 2
	if st.Peers == nil {
		st.Peers = map[string]string{}
	}
	if st.Committed == nil {
		st.Committed = map[string]string{}
	}
	payload, err := json.Marshal(st)
	if err != nil {
		return errors.New("序列化收信人状态失败")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建状态目录：%w", err)
	}
	tmp, err := os.CreateTemp(dir, ".dm-recipient-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件：%w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入收信人状态：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("刷盘收信人状态：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件：%w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("替换收信人状态：%w", err)
	}
	return nil
}

// lock 用排他创建锁文件实现跨进程互斥；只清理超过安全窗口的崩溃遗留锁。
//
// Windows 在锁文件高频创建/删除期间会瞬时返回 ERROR_ACCESS_DENIED（映射为 fs.ErrPermission），
// 这是文件系统竞争而非权限故障，必须继续等待，否则并发预占会被误判为锁超时。
func (s *RecipientStore) lock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return nil, err
	}
	lockPath := s.path + ".lock"
	deadline := time.Now().Add(recipientLockWait)
	var lastErr error
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = file.Close()
			return func() { removeRecipientLock(lockPath) }, nil
		}
		if !lockContended(err) {
			return nil, err
		}
		lastErr = err
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > recipientLockStale {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("收信人状态锁等待超过%v：%w", recipientLockWait, lastErr)
		}
		time.Sleep(recipientLockPoll)
	}
}

// lockContended 判断错误是否属于可重试的锁竞争。
func lockContended(err error) bool {
	return errors.Is(err, fs.ErrExist) || errors.Is(err, fs.ErrPermission)
}

// removeRecipientLock 删除锁文件；Windows 句柄或过滤驱动未及时释放时会短暂拒绝，
// 重试后仍失败就保留锁文件，交给 stale 清理兜底。
func removeRecipientLock(path string) {
	for attempt := 0; attempt < 5; attempt++ {
		err := os.Remove(path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return
		}
		time.Sleep(recipientLockPoll)
	}
}

// normalizeRecipient 补齐映射并把旧格式的单条记录并入无 actor 槽位。
func normalizeRecipient(st recipientState) recipientState {
	if st.Peers == nil {
		st.Peers = map[string]string{}
	}
	if st.Committed == nil {
		st.Committed = map[string]string{}
	}
	if st.Peer != "" {
		if _, ok := st.Peers[""]; !ok {
			st.Peers[""] = st.Peer
		}
	}
	return st
}

// forDay 跨天重置；同一天补齐缺失字段。
func (st recipientState) forDay(day string) recipientState {
	if st.Date != day {
		return recipientState{Version: 2, Date: day, Peers: map[string]string{}, Committed: map[string]string{}}
	}
	st.Version = 2
	if st.Peers == nil {
		st.Peers = map[string]string{}
	}
	if st.Committed == nil {
		st.Committed = map[string]string{}
	}
	if st.Peer != "" {
		if _, ok := st.Peers[""]; !ok {
			st.Peers[""] = st.Peer
		}
	}
	return st
}

// dayKey 返回 UTC 日期键。
func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }
