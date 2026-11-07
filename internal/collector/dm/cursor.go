package dm

// 本文件把私信水位落到文件：`state/dm-cursors.json`。
//
// 水位按「账号 + 会话 + 消息序列号(seqno)」保存：
//   - watermark：已确认连续覆盖到的最大 seqno；
//   - frontier：已产出的最大 seqno，可能高于 watermark（表示还有待回填缺口）；
//   - pending_from：缺口上沿（不含），空表示没有缺口。
//
// 写入用「临时文件 + Sync + Rename」保证原子；坏文件按「没有水位」处理，
// 宁可重扫历史，也不要永久卡死采集。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const defaultCursorPath = "state/dm-cursors.json"

// Cursor 是单个会话的水位快照。
type Cursor struct {
	Watermark   string `json:"watermark,omitempty"`
	Frontier    string `json:"frontier,omitempty"`
	PendingFrom string `json:"pending_from,omitempty"`
}

// CursorStore 按账号读写会话水位。
type CursorStore interface {
	Load(account string) (map[string]Cursor, error)
	Save(account string, cursors map[string]Cursor) error
}

type cursorFile struct {
	Version  int                          `json:"version"`
	Accounts map[string]map[string]Cursor `json:"accounts"`
}

// FileCursorStore 是跨重启保留的水位存储。
type FileCursorStore struct {
	path string
	mu   sync.Mutex
}

// NewFileCursorStore 只记录路径，不创建文件；空路径用 state/dm-cursors.json。
func NewFileCursorStore(path string) (*FileCursorStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultCursorPath
	}
	if strings.ContainsAny(path, "\x00\r\n") {
		return nil, errors.New("私信水位文件路径非法")
	}
	return &FileCursorStore{path: path}, nil
}

// Load 读取账号下的会话水位；文件不存在或损坏时返回空表。
func (s *FileCursorStore) Load(account string) (map[string]Cursor, error) {
	if s == nil || s.path == "" {
		return map[string]Cursor{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(account)
}

func (s *FileCursorStore) loadLocked(account string) (map[string]Cursor, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Cursor{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取私信水位：%w", err)
	}
	var file cursorFile
	if err := json.Unmarshal(data, &file); err != nil {
		// 坏文件当作没有水位：宁可重扫历史，也不要永久卡死采集
		return map[string]Cursor{}, nil
	}
	out := make(map[string]Cursor, len(file.Accounts[account]))
	for peer, cur := range file.Accounts[account] {
		if peer == "" || !validCursor(cur) {
			continue
		}
		out[peer] = cur
	}
	return out, nil
}

// Save 原子写入指定账号的水位，保留其他账号的数据。
func (s *FileCursorStore) Save(account string, cursors map[string]Cursor) error {
	if s == nil || s.path == "" {
		return errors.New("私信水位存储未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	file := cursorFile{Version: 1, Accounts: map[string]map[string]Cursor{}}
	if data, err := os.ReadFile(s.path); err == nil {
		var existing cursorFile
		if json.Unmarshal(data, &existing) == nil {
			file = existing
		}
	}
	if file.Accounts == nil {
		file.Accounts = map[string]map[string]Cursor{}
	}
	file.Version = 1
	file.Accounts[account] = cursors

	payload, err := json.Marshal(file)
	if err != nil {
		return errors.New("序列化私信水位失败")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建私信水位目录：%w", err)
	}
	tmp, err := os.CreateTemp(dir, ".dm-cursors-*.tmp")
	if err != nil {
		return fmt.Errorf("创建私信水位临时文件：%w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入私信水位：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("刷盘私信水位：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭私信水位临时文件：%w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("替换私信水位：%w", err)
	}
	return nil
}

// MemoryCursorStore 是进程内实现，供测试与未配置文件路径的装配使用。
type MemoryCursorStore struct {
	mu       sync.Mutex
	accounts map[string]map[string]Cursor
}

// NewMemoryCursorStore 构造空的内存水位存储。
func NewMemoryCursorStore() *MemoryCursorStore {
	return &MemoryCursorStore{accounts: make(map[string]map[string]Cursor)}
}

// Load 返回账号水位的副本。
func (s *MemoryCursorStore) Load(account string) (map[string]Cursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneCursors(s.accounts[account]), nil
}

// Save 覆盖账号水位。
func (s *MemoryCursorStore) Save(account string, cursors map[string]Cursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[account] = cloneCursors(cursors)
	return nil
}

func cloneCursors(src map[string]Cursor) map[string]Cursor {
	out := make(map[string]Cursor, len(src))
	for peer, cur := range src {
		out[peer] = cur
	}
	return out
}

func validCursor(cur Cursor) bool {
	return validDecimal(cur.Watermark) && validDecimal(cur.Frontier) && validDecimal(cur.PendingFrom)
}

func validDecimal(value string) bool {
	if value == "" {
		return true
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
