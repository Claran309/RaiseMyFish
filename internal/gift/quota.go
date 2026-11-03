package gift

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

// QuotaStore 记录每个本地自然日已真实送出的礼物数量。
// 长驻进程可用 NodeSender 的内存计数；一次性 CLI 必须注入文件账本，
// 否则每次启动都会重置计数，daily_limit 形同虚设。
type QuotaStore interface {
	Reserve(day string, num, limit int) error
	Release(day string, num int)
}

const (
	quotaLockWait  = 2 * time.Second
	quotaLockStale = 30 * time.Second
	quotaLockPoll  = 10 * time.Millisecond
)

// FileQuota 把 {day, used} 原子写入本地 JSON；跨进程用 O_EXCL 锁文件串行化。
type FileQuota struct {
	path string
	mu   sync.Mutex
}

type quotaLedger struct {
	Day  string `json:"day"`
	Used int    `json:"used"`
}

// NewFileQuota 只校验路径；目录在首次 Reserve 时创建。
func NewFileQuota(path string) (*FileQuota, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\r\n\x00") {
		return nil, errors.New("送礼额度账本路径无效")
	}
	return &FileQuota{path: path}, nil
}

// Reserve 在真实送礼前预占额度；账本损坏或锁等待超时都拒绝发送。
func (q *FileQuota) Reserve(day string, num, limit int) error {
	if num < 1 || limit < 1 {
		return &Error{Kind: KindFatal, Message: "送礼额度参数无效"}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	unlock, err := q.lock()
	if err != nil {
		return &Error{Kind: KindFatal, Message: "送礼额度账本被占用", Cause: err}
	}
	defer unlock()

	ledger, err := q.load()
	if err != nil {
		return &Error{Kind: KindFatal, Message: "送礼额度账本不可用", Cause: err}
	}
	if ledger.Day != day {
		ledger = quotaLedger{Day: day}
	}
	if ledger.Used+num > limit {
		return &Error{Kind: KindFatal, Message: "送礼超过每日限额"}
	}
	ledger.Used += num
	if err := q.save(ledger); err != nil {
		return &Error{Kind: KindFatal, Message: "送礼额度账本不可用", Cause: err}
	}
	return nil
}

// Release 只用于确定未提交 POST 的失败；写失败时保留占用，宁可少送不可多送。
func (q *FileQuota) Release(day string, num int) {
	if num < 1 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	unlock, err := q.lock()
	if err != nil {
		return
	}
	defer unlock()
	ledger, err := q.load()
	if err != nil || ledger.Day != day {
		return
	}
	ledger.Used -= num
	if ledger.Used < 0 {
		ledger.Used = 0
	}
	_ = q.save(ledger)
}

// Used 返回某天已真实送出的数量（只读；跨天返回 0）。
//
// 供 ReAct 的 quota 工具查询剩余额度用，不修改账本。
func (q *FileQuota) Used(day string) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ledger, err := q.load()
	if err != nil {
		return 0, err
	}
	if ledger.Day != day {
		return 0, nil
	}
	return ledger.Used, nil
}

func (q *FileQuota) load() (quotaLedger, error) {
	var ledger quotaLedger
	data, err := os.ReadFile(q.path)
	if errors.Is(err, fs.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return ledger, err
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ledger, err
	}
	if ledger.Used < 0 {
		return ledger, errors.New("账本计数为负")
	}
	return ledger, nil
}

func (q *FileQuota) save(ledger quotaLedger) (result error) {
	dir := filepath.Dir(q.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".gift-quota-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), q.path)
}

// lock 用排他创建锁文件实现跨进程互斥；只清理超过安全窗口的崩溃遗留锁。
//
// Windows 在锁文件高频创建/删除期间会瞬时返回 ERROR_ACCESS_DENIED（映射为 fs.ErrPermission），
// 这是文件系统竞争而非权限故障，必须继续等待，否则并发预占会被误判为锁超时。
func (q *FileQuota) lock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(q.path), 0700); err != nil {
		return nil, err
	}
	lockPath := q.path + ".lock"
	deadline := time.Now().Add(quotaLockWait)
	var lastErr error
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_ = file.Close()
			return func() { removeLockFile(lockPath) }, nil
		}
		if !lockContended(err) {
			return nil, err
		}
		lastErr = err
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > quotaLockStale {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("额度账本锁等待超过%v：%w", quotaLockWait, lastErr)
		}
		time.Sleep(quotaLockPoll)
	}
}

// lockContended 判断错误是否属于可重试的锁竞争。
func lockContended(err error) bool {
	return errors.Is(err, fs.ErrExist) || errors.Is(err, fs.ErrPermission)
}

// removeLockFile 删除锁文件；Windows 句柄或过滤驱动未及时释放时会短暂拒绝，
// 重试后仍失败就保留锁文件，交给 stale 清理兜底。
func removeLockFile(path string) {
	for attempt := 0; attempt < 5; attempt++ {
		err := os.Remove(path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return
		}
		time.Sleep(quotaLockPoll)
	}
}

var _ QuotaStore = (*FileQuota)(nil)
