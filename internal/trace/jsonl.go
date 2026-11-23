package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// jsonlWriter 只追加，不轮转；容量由 fishd 日志轮转/运维负责。
type jsonlWriter struct {
	mu   sync.Mutex
	file *os.File
}

func newJSONLWriter(path string) (*jsonlWriter, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &jsonlWriter{file: file}, nil
}

func (w *jsonlWriter) write(data []byte) {
	if w == nil || len(data) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.file.Write(append(data, '\n'))
}

func (w *jsonlWriter) close() error {
	if w == nil || w.file == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

func spanJSON(span *Span) []byte {
	copy := *span
	copy.rec = nil
	data, err := json.Marshal(copy)
	if err != nil {
		return nil
	}
	return data
}
