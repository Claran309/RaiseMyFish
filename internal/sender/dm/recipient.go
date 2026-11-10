package dm

// 本文件把「当日收信人」持久化为 actor/day 预占：`state/dm-recipient.json`。
//
// 语义：
//   - 键 = 发送账号（B站 MID）+ UTC 日期；每个账号每天最多 1 个收信人；
//   - 预占在 POST 前原子写入：进程内 mutex + O_EXCL 锁文件 + 临时文件 rename；
//   - 每笔发送尝试持有独立令牌，只有令牌持有者能在「明确未发出」后释放自己的预占；
//   - 预占状态 pending / unknown / committed：unknown（POST 结果未知）与 committed
//     粘住当天名额，后续失败尝试不能释放；
//   - 跨天自动重置；旧格式 {date, peer} 与 v2 {peers, committed} 继续可读，
//     无令牌的旧记录按 unknown 处理（无法确认是否已送达，不可随意释放）。

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

// recipientVersion 是当前落盘结构版本。
const recipientVersion = 3

// 预占状态。
const (
	reservationPending   = "pending"
	reservationUnknown   = "unknown"
	reservationCommitted = "committed"
)

// recipientState 是落盘结构。
type recipientState struct {
	Version int `json:"version,omitempty"`
	// Date 是 UTC 日期（YYYY-MM-DD）。
	Date string `json:"date"`
	// Peer 是旧格式/无 actor 场景下的收信人，保留以兼容既有状态文件。
	Peer string `json:"peer,omitempty"`
	// Peers 是 actor(MID) → 当天收信人，是 Reservations 的可读镜像。
	Peers map[string]string `json:"peers,omitempty"`
	// Committed 是已成功发送的 actor → 收信人，是 Reservations 的可读镜像。
	Committed map[string]string `json:"committed,omitempty"`
	// Reservations 是 actor → 预占记录；状态与尝试令牌以这里为准。
	Reservations map[string]reservationState `json:"reservations,omitempty"`
}

// reservationState 是单个 actor 当天的预占。
//
// Attempts 记录仍可释放的尝试令牌；只有令牌在列表里的尝试才能释放预占。
// unknown/committed 不保留令牌，任何失败尝试都无法解除。
type reservationState struct {
	Peer     string   `json:"peer"`
	State    string   `json:"state"`
	Attempts []string `json:"attempts,omitempty"`
}

// withAttempt 登记一笔在途尝试（同令牌重复登记保持幂等）。
func (r reservationState) withAttempt(token string) reservationState {
	for _, existing := range r.Attempts {
		if existing == token {
			return r
		}
	}
	r.Attempts = append(r.Attempts, token)
	return r
}

// removeAttempt 只删除本尝试的令牌；返回是否确实由本令牌持有。
func (r *reservationState) removeAttempt(token string) bool {
	for i, existing := range r.Attempts {
		if existing == token {
			r.Attempts = append(r.Attempts[:i], r.Attempts[i+1:]...)
			return true
		}
	}
	return false
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
// attempt 是本次发送尝试的令牌，只有它能释放本次预占。
func (s *RecipientStore) Reserve(day, actor, peer, attempt string) error {
	day, actor, peer = strings.TrimSpace(day), strings.TrimSpace(actor), strings.TrimSpace(peer)
	attempt = strings.TrimSpace(attempt)
	if s == nil || s.path == "" {
		return errors.New("收信人状态存储未初始化")
	}
	if day == "" || peer == "" || attempt == "" {
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
	rec, ok := st.Reservations[actor]
	if ok && rec.Peer != peer {
		return &Error{Kind: KindInput, Cause: errRecipientLocked}
	}
	if !ok {
		rec = reservationState{Peer: peer, State: reservationPending}
	}
	switch rec.State {
	case reservationPending:
		rec = rec.withAttempt(attempt)
	case reservationUnknown, reservationCommitted:
		// unknown/committed 不可释放；同人可继续尝试，但不登记可释放令牌。
	default:
		rec.State = reservationUnknown
		rec.Attempts = nil
	}
	st.Reservations[actor] = rec
	st.Peers[actor] = peer
	if actor == "" {
		st.Peer = peer
	}
	if err := s.saveLocked(st); err != nil {
		return fmt.Errorf("保存收信人预占：%w", err)
	}
	return nil
}

// MarkUnknown 把本尝试结算为「结果未知（可能已送达）」。
//
// 即使预占记录缺失也补写 unknown：宁可占住当天名额，也不能让后续失败尝试释放。
func (s *RecipientStore) MarkUnknown(day, actor, peer, attempt string) {
	day, actor, peer = strings.TrimSpace(day), strings.TrimSpace(actor), strings.TrimSpace(peer)
	attempt = strings.TrimSpace(attempt)
	if s == nil || s.path == "" || day == "" || peer == "" || attempt == "" {
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
	st = st.forDay(day)
	rec, ok := st.Reservations[actor]
	if ok && rec.Peer != peer {
		return
	}
	if !ok {
		rec = reservationState{Peer: peer}
	}
	if rec.State != reservationCommitted {
		rec.State = reservationUnknown
		rec.Attempts = nil
	}
	st.Reservations[actor] = rec
	st.Peers[actor] = peer
	if actor == "" {
		st.Peer = peer
	}
	_ = s.saveLocked(st)
}

// Commit 在发送成功后锁定当天名额；写盘失败由调用方按「已发但状态不确定」处理。
func (s *RecipientStore) Commit(day, actor, peer, attempt string) error {
	day, actor, peer = strings.TrimSpace(day), strings.TrimSpace(actor), strings.TrimSpace(peer)
	attempt = strings.TrimSpace(attempt)
	if s == nil || s.path == "" {
		return errors.New("收信人状态存储未初始化")
	}
	if day == "" || peer == "" || attempt == "" {
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
	st.Reservations[actor] = reservationState{Peer: peer, State: reservationCommitted}
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

// Release 只释放本尝试明确未发出 POST 的预占。
//
// 只有 pending 状态、且令牌仍登记在案的尝试才能释放；unknown/committed
// 与其它在途尝试的令牌都不受影响。写盘失败时保留占用（宁可少发）。
func (s *RecipientStore) Release(day, actor, peer, attempt string) {
	day, actor, peer = strings.TrimSpace(day), strings.TrimSpace(actor), strings.TrimSpace(peer)
	attempt = strings.TrimSpace(attempt)
	if s == nil || s.path == "" || day == "" || peer == "" || attempt == "" {
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
	rec, ok := st.Reservations[actor]
	if !ok || rec.Peer != peer || rec.State != reservationPending {
		return
	}
	if !rec.removeAttempt(attempt) {
		// 不是本尝试登记的令牌：可能是结果未知的旧尝试，绝不释放。
		return
	}
	if len(rec.Attempts) > 0 {
		st.Reservations[actor] = rec
		_ = s.saveLocked(st)
		return
	}
	delete(st.Reservations, actor)
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
	st.Version = recipientVersion
	if st.Peers == nil {
		st.Peers = map[string]string{}
	}
	if st.Committed == nil {
		st.Committed = map[string]string{}
	}
	if st.Reservations == nil {
		st.Reservations = map[string]reservationState{}
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

// normalizeRecipient 补齐映射、把旧格式并入 Reservations，并同步镜像字段。
//
// 没有令牌的旧记录无法确认是否已送达，一律按 unknown 处理（不可随意释放）。
func normalizeRecipient(st recipientState) recipientState {
	if st.Peers == nil {
		st.Peers = map[string]string{}
	}
	if st.Committed == nil {
		st.Committed = map[string]string{}
	}
	if st.Reservations == nil {
		st.Reservations = map[string]reservationState{}
	}
	if st.Peer != "" {
		if _, ok := st.Peers[""]; !ok {
			st.Peers[""] = st.Peer
		}
	}
	// 旧 peers 记录没有令牌：按 unknown 迁移，避免升级后被失败尝试释放。
	for actor, peer := range st.Peers {
		if peer == "" {
			continue
		}
		if _, ok := st.Reservations[actor]; !ok {
			st.Reservations[actor] = reservationState{Peer: peer, State: reservationUnknown}
		}
	}
	// committed 优先：旧字段与 reservations 冲突时按已发送处理。
	for actor, peer := range st.Committed {
		if peer == "" {
			continue
		}
		st.Reservations[actor] = reservationState{Peer: peer, State: reservationCommitted}
		st.Peers[actor] = peer
	}
	for actor, rec := range st.Reservations {
		rec.Peer = strings.TrimSpace(rec.Peer)
		switch rec.State {
		case reservationPending:
		case reservationUnknown, reservationCommitted:
			rec.Attempts = nil
		default:
			// 未知状态按最保守的 unknown 处理，不保留可释放令牌。
			rec.State = reservationUnknown
			rec.Attempts = nil
		}
		if rec.Peer == "" {
			delete(st.Reservations, actor)
			continue
		}
		st.Reservations[actor] = rec
		st.Peers[actor] = rec.Peer
		if rec.State == reservationCommitted {
			st.Committed[actor] = rec.Peer
		} else {
			delete(st.Committed, actor)
		}
	}
	for actor := range st.Peers {
		if _, ok := st.Reservations[actor]; !ok {
			delete(st.Peers, actor)
		}
	}
	for actor := range st.Committed {
		if _, ok := st.Reservations[actor]; !ok {
			delete(st.Committed, actor)
		}
	}
	return st
}

// forDay 跨天重置；同一天补齐缺失字段。
func (st recipientState) forDay(day string) recipientState {
	if st.Date != day {
		return recipientState{
			Version:      recipientVersion,
			Date:         day,
			Peers:        map[string]string{},
			Committed:    map[string]string{},
			Reservations: map[string]reservationState{},
		}
	}
	st.Version = recipientVersion
	return normalizeRecipient(st)
}

// dayKey 返回 UTC 日期键。
func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }
