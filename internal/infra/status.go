package infra

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Status 仅含运维摘要，不包含DSN、密钥或消息内容。
// Presence 是活动流的一句话摘要（P0 内存态，随心跳落盘供 CLI 读取）。
type Status struct {
	PID       int       `json:"pid"`
	State     string    `json:"state"`
	Database  string    `json:"database"`
	Version   string    `json:"version"`
	Presence  string    `json:"presence,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Alive 同时验证状态、心跳时效与数据库状态，不把过期文件当活进程。
func (s Status) Alive(now time.Time, ttl time.Duration) bool {
	age := now.Sub(s.UpdatedAt)
	return s.PID > 0 && s.State == "running" && s.Database == "ready" && age >= 0 && age <= ttl
}

// WriteStatus 在同目录临时文件写完后替换；取消/崩溃不留下半段JSON。
func WriteStatus(path string, state Status) (result error) {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("编码状态摘要：%w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建状态目录：%w", err)
	}
	f, err := os.CreateTemp(dir, ".fishd-status-*")
	if err != nil {
		return fmt.Errorf("创建临时状态文件：%w", err)
	}
	defer func() {
		if err := os.Remove(f.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("清理临时状态：%w", err))
		}
	}()
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("写入状态摘要：%w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("替换状态摘要：%w", err)
	}
	return nil
}

// ReadStatus 读取本机状态，调用方必须检查时效。
func ReadStatus(path string) (Status, error) {
	var state Status
	data, err := os.ReadFile(path)
	if err != nil {
		return state, fmt.Errorf("读取服务状态：%w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("状态文件格式错误：%w", err)
	}
	return state, nil
}
