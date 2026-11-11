package msgfeed

// 本文件把消息监听水位落到文件：`state/msgfeed-cursors.json`。
//
// 水位按账号保存两类消息的 (时间, 通知ID)：
//   - 回复我的（reply）；@我的（at）。
//   - Initialized=false 表示还没建立基线，首次拉取只定基线、不补历史。
//   - 平台 last_view_at 只作基线与观测，不参与增量跳过；已读消息照常拉取。
//
// 写入用「临时文件 + Sync + Rename」保证原子；坏文件按「没有水位」处理，
// 重新走首次基线，宁可漏一段历史，也不给旧消息批量回复。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const defaultCursorPath = "state/msgfeed-cursors.json"

// Cursor 是一个账号的消息监听水位。
//
// ReplyTime/AtTime 是「已经处理到哪」的时间水位，只在事件全部落库确认后推进；
// LastViewAt 是平台返回的「上次查看时间」，仅用于首次基线、顶号观测与日志，
// 绝不拿来跳过消息——主人顶号把消息标已读时，游标仍按已处理水位继续拉。
type Cursor struct {
	Initialized bool   `json:"initialized"`
	ReplyTime   int64  `json:"reply_time,omitempty"`
	ReplyID     string `json:"reply_id,omitempty"`
	AtTime      int64  `json:"at_time,omitempty"`
	AtID        string `json:"at_id,omitempty"`
	LastViewAt  int64  `json:"last_view_at,omitempty"`
}

// CursorStore 按账号读写水位。
type CursorStore interface {
	Load(account string) (Cursor, error)
	Save(account string, cursor Cursor) error
}

type cursorFile struct {
	Version  int               `json:"version"`
	Accounts map[string]Cursor `json:"accounts"`
}

// FileCursorStore 是跨重启保留的水位存储。
type FileCursorStore struct {
	path string
	mu   sync.Mutex
}

// NewFileCursorStore 只记录路径，不创建文件；空路径用 state/msgfeed-cursors.json。
func NewFileCursorStore(path string) (*FileCursorStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultCursorPath
	}
	if strings.ContainsAny(path, "\x00\r\n") {
		return nil, errors.New("消息监听水位文件路径非法")
	}
	return &FileCursorStore{path: path}, nil
}

// Load 读取账号水位；文件不存在或损坏时返回空水位。
func (s *FileCursorStore) Load(account string) (Cursor, error) {
	if s == nil || s.path == "" {
		return Cursor{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Cursor{}, nil
	}
	if err != nil {
		return Cursor{}, fmt.Errorf("读取消息监听水位：%w", err)
	}
	var file cursorFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Cursor{}, nil
	}
	cursor := file.Accounts[account]
	if !validCursor(cursor) {
		return Cursor{}, nil
	}
	return cursor, nil
}

// Save 原子写入指定账号的水位，保留其他账号的数据。
func (s *FileCursorStore) Save(account string, cursor Cursor) error {
	if s == nil || s.path == "" {
		return errors.New("消息监听水位存储未初始化")
	}
	if !validCursor(cursor) {
		return errors.New("消息监听水位非法")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	file := cursorFile{Version: 1, Accounts: map[string]Cursor{}}
	if data, err := os.ReadFile(s.path); err == nil {
		var existing cursorFile
		if json.Unmarshal(data, &existing) == nil {
			file = existing
		}
	}
	if file.Accounts == nil {
		file.Accounts = map[string]Cursor{}
	}
	file.Version = 1
	file.Accounts[account] = cursor

	payload, err := json.Marshal(file)
	if err != nil {
		return errors.New("序列化消息监听水位失败")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建消息监听水位目录：%w", err)
	}
	tmp, err := os.CreateTemp(dir, ".msgfeed-cursors-*.tmp")
	if err != nil {
		return fmt.Errorf("创建消息监听水位临时文件：%w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入消息监听水位：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("刷盘消息监听水位：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭消息监听水位临时文件：%w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("替换消息监听水位：%w", err)
	}
	return nil
}

// MemoryCursorStore 是进程内实现，供测试使用。
type MemoryCursorStore struct {
	mu       sync.Mutex
	accounts map[string]Cursor
}

func NewMemoryCursorStore() *MemoryCursorStore {
	return &MemoryCursorStore{accounts: map[string]Cursor{}}
}

func (s *MemoryCursorStore) Load(account string) (Cursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accounts[account], nil
}

func (s *MemoryCursorStore) Save(account string, cursor Cursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[account] = cursor
	return nil
}

func validCursor(cursor Cursor) bool {
	if cursor.ReplyTime < 0 || cursor.AtTime < 0 || cursor.LastViewAt < 0 {
		return false
	}
	return validID(cursor.ReplyID) && validID(cursor.AtID)
}

func validID(id string) bool {
	if id == "" {
		return true
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
