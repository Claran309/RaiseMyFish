package extractworker

// 本文件放提炼水位（游标）的两种实现。
//
// 水位语义：已处理到的「事件时间 + 事件 ID」位置，下一轮从这里往后接着扫。
// 之所以要持久化：进程重启后不能从头重扫（幂等能挡住重复行，但白费扫描）也不能跳过积压。
//
// 为什么不放数据库：加表要改迁移，迁移归 DSH/Codex；本批只提供接口 + 文件实现，
// 生产用哪个路径由装配层决定。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// watermarkVersion 是水位文件格式版本，便于以后演进时识别旧文件。
const watermarkVersion = 1

// fileWatermarkState 是落盘的 JSON 结构；不含正文、不含凭据。
type fileWatermarkState struct {
	Version int       `json:"version"`
	At      time.Time `json:"at"`
	ID      string    `json:"id"`
}

// MemoryWatermark 是进程内水位。
//
// 只适合测试与开发路径：进程一退出就丢，重启会从头扫（靠落库幂等兜住，但会多扫）。
// 生产请用 FileWatermark 或自行实现 Watermark。
type MemoryWatermark struct {
	mu     sync.Mutex
	cursor Cursor
}

// NewMemoryWatermark 创建进程内水位。
func NewMemoryWatermark() *MemoryWatermark { return &MemoryWatermark{} }

// Load 读取当前水位；零值表示从头开始。
func (m *MemoryWatermark) Load(ctx context.Context) (Cursor, error) {
	if ctx == nil {
		return Cursor{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return Cursor{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursor, nil
}

// Save 覆盖写入水位。
func (m *MemoryWatermark) Save(ctx context.Context, c Cursor) error {
	if ctx == nil {
		return errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursor = c
	return nil
}

// FileWatermark 把水位存在本机 JSON 文件里，重启后能接着补提炼。
type FileWatermark struct {
	path string
}

// NewFileWatermark 创建文件水位；路径由装配层给出（例如放在 fishd 的状态目录下）。
func NewFileWatermark(path string) (*FileWatermark, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("水位文件路径不能为空")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析水位文件路径：%w", err)
	}
	return &FileWatermark{path: filepath.Clean(abs)}, nil
}

// Load 读取水位；文件不存在视为「首次运行」，返回零游标而不是错误。
func (f *FileWatermark) Load(ctx context.Context) (Cursor, error) {
	if ctx == nil {
		return Cursor{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return Cursor{}, err
	}

	data, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Cursor{}, nil
		}
		return Cursor{}, fmt.Errorf("读取提炼水位：%w", err)
	}

	var state fileWatermarkState
	if err := json.Unmarshal(data, &state); err != nil {
		// 水位文件损坏时明确报错，不能静默当零值——那会变成重复扫描而不自知
		return Cursor{}, fmt.Errorf("提炼水位文件格式错误：%w", err)
	}
	if state.Version != watermarkVersion {
		return Cursor{}, fmt.Errorf("提炼水位文件版本不支持：%d", state.Version)
	}
	return Cursor{At: state.At, ID: state.ID}, nil
}

// Save 原子写入水位：先写同目录临时文件再 rename，崩溃不会留下半段 JSON。
func (f *FileWatermark) Save(ctx context.Context, c Cursor) (result error) {
	if ctx == nil {
		return errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	data, err := json.Marshal(fileWatermarkState{Version: watermarkVersion, At: c.At, ID: c.ID})
	if err != nil {
		return fmt.Errorf("编码提炼水位：%w", err)
	}

	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建水位目录：%w", err)
	}
	tmp, err := os.CreateTemp(dir, ".extract-watermark-*")
	if err != nil {
		return fmt.Errorf("创建临时水位文件：%w", err)
	}
	defer func() {
		if err := os.Remove(tmp.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("清理临时水位文件：%w", err))
		}
	}()

	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("写入提炼水位：%w", err)
	}
	if err := os.Rename(tmp.Name(), f.path); err != nil {
		return fmt.Errorf("替换提炼水位：%w", err)
	}
	return nil
}
