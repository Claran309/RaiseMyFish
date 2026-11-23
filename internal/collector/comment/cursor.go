package comment

// 本文件把评论轮询水位落到文件：`state/comment-cursors.json`。
//
// 水位按「评论类型 + OID」保存 (ctime, rpid)：
//   - ctime：已连续覆盖到的最旧评论时间；
//   - rpid：同秒内的单调游标；
//   - covered：是否完成过一次到达末尾的扫描；未覆盖的目标重启后仍从头补。
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

const defaultCursorPath = "state/comment-cursors.json"

// Cursor 是单个目标的评论水位。
type Cursor struct {
	At      int64  `json:"at,omitempty"`
	ID      string `json:"id,omitempty"`
	Covered bool   `json:"covered,omitempty"`
}

// CursorStore 读写评论水位；key 是 targetKey（type:oid）。
type CursorStore interface {
	Load() (map[string]Cursor, error)
	Save(map[string]Cursor) error
}

type cursorFile struct {
	Version int               `json:"version"`
	Targets map[string]Cursor `json:"targets"`
}

// FileCursorStore 是跨重启保留的评论水位存储。
type FileCursorStore struct {
	path string
	mu   sync.Mutex
}

// NewFileCursorStore 只记录路径，不创建文件；空路径用 state/comment-cursors.json。
func NewFileCursorStore(path string) (*FileCursorStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultCursorPath
	}
	if strings.ContainsAny(path, "\x00\r\n") {
		return nil, errors.New("评论水位文件路径非法")
	}
	return &FileCursorStore{path: path}, nil
}

// Load 读取水位；文件不存在或损坏时返回空表并给出错误（损坏时）。
func (s *FileCursorStore) Load() (map[string]Cursor, error) {
	if s == nil || s.path == "" {
		return map[string]Cursor{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Cursor{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取评论水位：%w", err)
	}
	var file cursorFile
	if err := json.Unmarshal(data, &file); err != nil {
		return map[string]Cursor{}, fmt.Errorf("评论水位文件损坏：%w", err)
	}
	if file.Targets == nil {
		file.Targets = map[string]Cursor{}
	}
	return file.Targets, nil
}

// Save 原子写入全部目标水位。
func (s *FileCursorStore) Save(cursors map[string]Cursor) error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("创建评论水位目录：%w", err)
	}
	data, err := json.Marshal(cursorFile{Version: 1, Targets: cursors})
	if err != nil {
		return fmt.Errorf("序列化评论水位：%w", err)
	}
	tmp := s.path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("写入评论水位：%w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(tmp)
		return fmt.Errorf("写入评论水位：%w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmp)
		return fmt.Errorf("同步评论水位：%w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("关闭评论水位：%w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("替换评论水位：%w", err)
	}
	return nil
}
