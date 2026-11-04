package dm

// 本文件把「当日收信人」从进程内状态落到文件：`state/dm-recipient.json`。
//
// 语义（派发单 #3）：
//   - 文件内容 `{date: "YYYY-MM-DD", peer: "uid"}`，日期按 **UTC** 算；
//   - 同一天 + 不同 peer → 拒绝；同 peer → 放行；跨天 → 允许并覆盖；
//   - 只有发送成功后才写文件（失败不占用当天名额）；
//   - 写入用「临时文件 + Sync + Rename」保证原子，进程内用 mutex 串行。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// recipientState 是落盘结构。
type recipientState struct {
	// Date 是 UTC 日期（YYYY-MM-DD）。
	Date string `json:"date"`
	// Peer 是当天已锁定的收信人 uid。
	Peer string `json:"peer"`
}

// RecipientStore 是当日收信人的持久化存储。
type RecipientStore struct {
	path string
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

// Load 读取状态；文件不存在返回零值（不报错）。
func (s *RecipientStore) Load() (recipientState, error) {
	if s == nil || s.path == "" {
		return recipientState{}, nil
	}
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
	return st, nil
}

// Save 原子写入：临时文件 → Sync → Rename。
func (s *RecipientStore) Save(st recipientState) error {
	if s == nil || s.path == "" {
		return errors.New("收信人状态存储未初始化")
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

// dayKey 返回 UTC 日期键。
func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }
