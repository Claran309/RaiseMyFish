package infra

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestLoggerDualOutputRotationAndRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	for _, name := range []string{"fishd-2026-10-20.log", "fishd-2026-10-29.log", "other.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stderr bytes.Buffer
	log, closeLog, err := newLogger("info", dir, &stderr, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("不应写出")
	log.Info("服务启动", zap.String("action", "start"))
	now = now.Add(2 * time.Minute)
	log.Info("跨日运行", zap.String("action", "heartbeat"))
	if err := closeLog(); err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{"2026-10-31", "2026-11-01"} {
		data, err := os.ReadFile(filepath.Join(dir, "fishd-"+day+".log"))
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			Level   string `json:"level"`
			Action  string `json:"action"`
			Message string `json:"msg"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
			t.Fatal(err)
		}
		if record.Level != "info" || record.Action == "" || record.Message == "" {
			t.Fatalf("JSON字段缺失")
		}
		if !bytes.Contains(stderr.Bytes(), bytes.TrimSpace(data)) {
			t.Fatal("stderr与文件内容不一致")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "fishd-2026-10-20.log")); !os.IsNotExist(err) {
		t.Fatal("过期日志未清理")
	}
	if _, err := os.Stat(filepath.Join(dir, "other.log")); err != nil {
		t.Fatal("不应删除无关文件")
	}
	if strings.Contains(stderr.String(), "不应写出") {
		t.Fatal("级别过滤未生效")
	}
}

func TestStateFreshnessAndAtomicReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "status.json")
	now := time.Now().UTC()
	state := Status{PID: 123, State: "running", Database: "ready", UpdatedAt: now}
	if err := WriteStatus(path, state); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Alive(now, 20*time.Second) {
		t.Fatal("新心跳被判过期")
	}
	if got.Alive(now.Add(21*time.Second), 20*time.Second) {
		t.Fatal("过期心跳被当作在线")
	}
	if got.Alive(now.Add(-time.Hour), 20*time.Second) {
		t.Fatal("未来时间不应被当作在线")
	}
	state.State = "stopped"
	if err := WriteStatus(path, state); err != nil {
		t.Fatal(err)
	}
	got, err = ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Alive(now, 20*time.Second) {
		t.Fatal("停止状态仍在线")
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(path); err == nil {
		t.Fatal("损坏状态没有报错")
	}
}

func TestConnectionErrorKeepsCauseWithoutLeakingSecrets(t *testing.T) {
	cause := errors.New("postgres://fish:private-secret@localhost/db")
	err := connectionError("PG探活失败", cause)
	if strings.Contains(err.Error(), "private-secret") || !errors.Is(err, cause) {
		t.Fatal("错误链或脱敏不正确")
	}
}
