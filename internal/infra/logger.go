package infra

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewLogger 创建按天滚动的JSON双输出日志；关闭函数只同步并关闭文件，不关闭stderr。
func NewLogger(level, dir string) (*zap.Logger, func() error, error) {
	return newLogger(level, dir, os.Stderr, time.Now)
}

func newLogger(level, dir string, stderr io.Writer, now func() time.Time) (*zap.Logger, func() error, error) {
	var threshold zapcore.Level
	if err := threshold.UnmarshalText([]byte(level)); err != nil {
		return nil, nil, fmt.Errorf("日志级别无效：%w", errors.New("不支持的级别"))
	}
	w := &dailyWriter{dir: dir, now: now}
	if err := w.rotate(); err != nil {
		return nil, nil, errors.Join(err, w.Close())
	}
	encoder := zap.NewProductionEncoderConfig()
	encoder.EncodeTime = zapcore.ISO8601TimeEncoder
	output := zapcore.AddSync(io.MultiWriter(w, stderr))
	logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(encoder), output, threshold))
	return logger, w.Close, nil
}

type dailyWriter struct {
	mu   sync.Mutex
	dir  string
	day  string
	file *os.File
	now  func() time.Time
}

// rotate只由持锁Write或构造函数调用；清理只匹配fishd日期日志。
func (w *dailyWriter) rotate() error {
	now := w.now()
	day := now.Format("2006-01-02")
	if day == w.day && w.file != nil {
		return nil
	}
	if err := os.MkdirAll(w.dir, 0700); err != nil {
		return fmt.Errorf("创建日志目录：%w", err)
	}
	f, err := os.OpenFile(filepath.Join(w.dir, "fishd-"+day+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("打开日志文件：%w", err)
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return errors.Join(fmt.Errorf("关闭旧日志：%w", err), f.Close())
		}
	}
	w.file = f
	w.day = day
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return fmt.Errorf("列出日志目录：%w", err)
	}
	cutoff := now.AddDate(0, 0, -6).Format("2006-01-02")
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "fishd-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		date := strings.TrimSuffix(strings.TrimPrefix(name, "fishd-"), ".log")
		if _, err := time.Parse("2006-01-02", date); err == nil && date < cutoff {
			if err := os.Remove(filepath.Join(w.dir, name)); err != nil {
				return fmt.Errorf("清理过期日志：%w", err)
			}
		}
	}
	return nil
}

func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.rotate(); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

func (w *dailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := errors.Join(w.file.Sync(), w.file.Close())
	w.file = nil
	if err != nil {
		return fmt.Errorf("同步并关闭日志：%w", err)
	}
	return nil
}
