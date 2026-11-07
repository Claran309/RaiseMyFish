// Package logview 只读 fishd 的 zap JSON 日志，供面板最近记录与 SSE 实时 tail 使用。
//
// 边界：
//   - 只按「目录 + fishd-YYYY-MM-DD.log」约定读文件，不接受请求方传入任意路径；
//   - 只输出白名单字段，未知字段一律丢弃；
//   - 输出前统一脱敏（cookie/token/secret 等），并且不读 stdout、不执行命令。
package logview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// maxReadBytes 是单次 Recent 从每个日志文件最多读的尾部字节数。
	maxReadBytes = 2 << 20
	// maxMessageRunes/maxFieldRunes 是展示截断上限。
	maxMessageRunes = 400
	maxFieldRunes   = 200
)

// Entry 是脱敏后的日志行。
type Entry struct {
	Time   string            `json:"time"`
	Level  string            `json:"level"`
	Msg    string            `json:"msg"`
	Action string            `json:"action,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
}

// Query 过滤日志：Actions 为空表示不限 action；Matches 为 OR 字段组。
type Query struct {
	Actions []string
	Matches []map[string]string
	Limit   int
}

// Source 是一个日志目录的只读视图；零值不可用，需 NewSource。
type Source struct {
	dir  string
	now  func() time.Time
	poll time.Duration
	// readChunk 是「从 offset 起读 length 字节」的实现，测试可替换以统计读取量；
	// 为零时用 readFrom（生产路径）。
	readChunk func(path string, offset, length int64) ([]byte, error)
}

// NewSource 绑定日志目录；now 仅在测试里替换。
func NewSource(dir string, now func() time.Time) *Source {
	if now == nil {
		now = time.Now
	}
	return &Source{dir: strings.TrimSpace(dir), now: now, poll: time.Second}
}

// Recent 读今天（不够再补昨天）的日志，按时间倒序返回匹配项。
func (s *Source) Recent(q Query) ([]Entry, error) {
	if s == nil || s.dir == "" {
		return nil, errors.New("日志目录未配置")
	}
	limit := q.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	now := s.now()
	paths := []string{s.pathFor(now), s.pathFor(now.AddDate(0, 0, -1))}
	entries := make([]Entry, 0, limit)
	for _, path := range paths {
		if len(entries) >= limit {
			break
		}
		data, err := readTail(path, maxReadBytes)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("读取日志失败：%w", err)
		}
		lines := bytes.Split(data, []byte("\n"))
		// 从后往前：日志天然按时间追加，倒序即最新优先。
		for i := len(lines) - 1; i >= 0 && len(entries) < limit; i-- {
			entry, ok := parseLine(lines[i])
			if !ok || !q.Match(entry) {
				continue
			}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// Tail 从当前日志末尾开始持续推送新行，返回时表示 ctx 取消或回调报错。
//
// 增量读取（O05）：每轮只 Stat 一次文件，再用 offset 读「新增的那段」。
// 旧实现每轮 os.ReadFile 整个日志——即使没有新行也在全量分配，
// 大日志（几十 MB）会让面板 SSE 请求持续吃内存。现在无新增时零读取。
//
// 轮转处理：
//   - 跨天：换成新日期的文件，从头读；
//   - 替换（inode 变了，如重命名+新建）：从头读；
//   - 截断（同一文件变小）：从头读。
//
// 轮询而不是 fsnotify：不引新依赖，日志量小，1s 延迟足够面板使用。
func (s *Source) Tail(ctx context.Context, fn func(Entry) error) error {
	if s == nil || s.dir == "" {
		return errors.New("日志目录未配置")
	}
	if fn == nil {
		return errors.New("日志回调不能为 nil")
	}
	poll := s.poll
	if poll <= 0 {
		poll = time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	read := s.readChunk
	if read == nil {
		read = readFrom
	}

	path := s.pathFor(s.now())
	var lastInfo os.FileInfo
	var offset int64
	if info, err := os.Stat(path); err == nil {
		// 从当前末尾开始：历史行不重放。
		lastInfo, offset = info, info.Size()
	}
	var pending []byte
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			next := s.pathFor(s.now())
			if next != path {
				// 跨天轮转：新文件从头读。
				path, offset, pending, lastInfo = next, 0, nil, nil
			}
			info, err := os.Stat(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return fmt.Errorf("读取日志失败：%w", err)
			}
			// 文件被替换（重命名 + 新建同名文件）：inode 变化。
			if lastInfo != nil && !os.SameFile(lastInfo, info) {
				offset, pending = 0, nil
			}
			lastInfo = info
			if info.Size() < offset {
				// 文件被截断：从头再来。
				offset, pending = 0, nil
			}
			if info.Size() == offset {
				// 无新增：不读、不分配。
				continue
			}
			chunk, err := read(path, offset, info.Size()-offset)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return fmt.Errorf("读取日志失败：%w", err)
			}
			offset += int64(len(chunk))
			pending = append(pending, chunk...)
			for {
				index := bytes.IndexByte(pending, '\n')
				if index < 0 {
					break
				}
				line := pending[:index]
				pending = pending[index+1:]
				entry, ok := parseLine(line)
				if !ok {
					continue
				}
				if err := fn(entry); err != nil {
					return err
				}
			}
		}
	}
}

// Match 判断一条日志是否命中 Query。
func (q Query) Match(e Entry) bool {
	if len(q.Actions) > 0 {
		found := false
		for _, action := range q.Actions {
			if e.Action == action {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(q.Matches) == 0 {
		return true
	}
	for _, group := range q.Matches {
		ok := true
		for key, want := range group {
			got, exists := e.Fields[key]
			if !exists || got != want {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (s *Source) pathFor(t time.Time) string {
	return filepath.Join(s.dir, "fishd-"+t.Format("2006-01-02")+".log")
}

// readFrom 从 offset 起读 length 字节；文件在此期间变大/变小都按实际读到的返回。
func readFrom(path string, offset, length int64) ([]byte, error) {
	if length <= 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, length)
	n, err := io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

// readTail 读取文件末尾 max 字节；截断处若落在行中间，丢弃首行残片。
func readTail(path string, max int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	start := int64(0)
	if size > max {
		start = size - max
	}
	if _, err := file.Seek(start, 0); err != nil {
		return nil, err
	}
	data := make([]byte, size-start)
	n, err := io.ReadFull(file, data)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	data = data[:n]
	if start > 0 {
		if index := bytes.IndexByte(data, '\n'); index >= 0 {
			data = data[index+1:]
		} else {
			data = nil
		}
	}
	return data, nil
}

// parseLine 解析一行 zap JSON；未知字段与非法 JSON 直接跳过。
func parseLine(line []byte) (Entry, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return Entry{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return Entry{}, false
	}
	entry := Entry{
		Time:  stringValue(raw["ts"]),
		Level: stringValue(raw["level"]),
		Msg:   clipText(stringValue(raw["msg"]), maxMessageRunes),
	}
	if entry.Level == "" && entry.Msg == "" {
		return Entry{}, false
	}
	entry.Action = stringValue(raw["action"])
	fields := make(map[string]string)
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !safeField(key) {
			continue
		}
		value := stringValue(raw[key])
		if value == "" {
			continue
		}
		fields[key] = clipText(Redact(value), maxFieldRunes)
	}
	if len(fields) > 0 {
		entry.Fields = fields
	}
	entry.Msg = Redact(entry.Msg)
	entry.Action = Redact(entry.Action)
	return entry, true
}

// safeField 是允许展示的 zap 字段白名单；凭据、prompt 与正文类字段不在其中。
var safeFields = map[string]struct{}{
	"action": {}, "reason": {}, "error": {}, "error_kind": {}, "pid": {},
	"room": {}, "mid": {}, "channel": {}, "event_id": {}, "tool": {}, "status": {},
	"interval_seconds": {}, "latency_ms": {}, "send_gap_seconds": {},
	"verified": {}, "react": {}, "runes": {}, "visual_runes": {},
	"degraded": {}, "failures": {}, "warnings": {}, "has_description": {}, "has_transcript": {},
	"scanned": {}, "extracted": {}, "inserted": {}, "duplicated": {}, "skipped": {},
	"learned": {}, "candidates": {}, "limited": {}, "frozen": {},
	"restarts": {}, "verdict": {}, "restarted": {}, "cooldown_seconds": {},
	"stale_seconds": {}, "threshold": {}, "model": {}, "steps": {}, "ok": {},
}

func safeField(key string) bool {
	_, ok := safeFields[key]
	return ok
}

func stringValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	var flag bool
	if err := json.Unmarshal(raw, &flag); err == nil {
		if flag {
			return "true"
		}
		return "false"
	}
	return ""
}

var (
	secretPattern = regexp.MustCompile(`(?i)(SESSDATA|bili_jct|buvid3|buvid4|authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|secret|cookie)\s*[=:]\s*(?:bearer\s+)?[^\s,;]+`)
	bearerPattern = regexp.MustCompile(`(?i)bearer\s+[^\s,;]+`)
)

// Redact 抹掉常见的凭据赋值；宁可多抹，不可漏出。
func Redact(text string) string {
	if text == "" {
		return text
	}
	text = secretPattern.ReplaceAllString(text, "$1=***")
	return bearerPattern.ReplaceAllString(text, "Bearer ***")
}

func clipText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "…"
}
