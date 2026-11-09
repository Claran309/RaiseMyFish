package logview

// 本文件测 N20 的有界读取：跨天轮转、无换行大行、慢消费追赶与行上限。

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func appendLog(t *testing.T, path, body string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func jsonLine(msg string) string {
	return `{"ts":"2026-11-05T10:00:00.000Z","level":"info","msg":"` + msg + `","action":"watch"}`
}

// TestTailCrossDayRotation 日期切换后换新文件从头读，旧文件不重放。
func TestTailCrossDayRotation(t *testing.T) {
	day1 := time.Date(2026, 11, 5, 23, 59, 0, 0, time.UTC)
	day2 := day1.Add(2 * time.Minute)
	dir := t.TempDir()
	writeLog(t, dir, day1, jsonLine("昨天旧行"))

	var mu sync.Mutex
	current := day1
	src := NewSource(dir, func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	})
	src.poll = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Entry, 4)
	done := make(chan error, 1)
	go func() {
		done <- src.Tail(ctx, func(e Entry) error {
			got <- e
			return nil
		})
	}()
	time.Sleep(30 * time.Millisecond)

	writeLog(t, dir, day2, jsonLine("跨天新行"))
	mu.Lock()
	current = day2
	mu.Unlock()

	select {
	case e := <-got:
		if e.Msg != "跨天新行" {
			t.Fatalf("跨天后应只推新文件内容：%+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("跨天后未读取新文件")
	}
	select {
	case e := <-got:
		t.Fatalf("不应重放旧文件：%+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消后应正常退出：%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Tail 未退出")
	}
}

// TestTailSkipsOversizedLineAndRecovers 无换行大行丢弃后，后续正常行仍要推送；
// 单次读取必须限制在 tailChunkBytes，而不是一次分配整段积压。
func TestTailSkipsOversizedLineAndRecovers(t *testing.T) {
	day := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := writeLog(t, dir, day, jsonLine("旧行"))
	src := NewSource(dir, func() time.Time { return day })
	src.poll = 10 * time.Millisecond
	reads := &tailReads{}
	src.readChunk = reads.read

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Entry, 4)
	done := make(chan error, 1)
	go func() {
		done <- src.Tail(ctx, func(e Entry) error {
			got <- e
			return nil
		})
	}()
	time.Sleep(30 * time.Millisecond)

	// 让超限行跨越多个 chunk，且本身是合法 JSON；超限后必须整行跳过。
	huge := jsonLine(strings.Repeat("x", tailMaxLineBytes+tailChunkBytes)) + "\n"
	appendLog(t, path, huge+jsonLine("恢复后的行")+"\n")

	select {
	case e := <-got:
		if e.Msg != "恢复后的行" {
			t.Fatalf("超限行应被跳过，首条应是恢复行：%+v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超限行之后的正常行未推送")
	}
	select {
	case e := <-got:
		t.Fatalf("超限行不应被推送：%+v", e)
	case <-time.After(50 * time.Millisecond):
	}

	if calls, _, maxLen := reads.snapshot(); calls < 2 || maxLen > tailChunkBytes {
		t.Fatalf("应按固定小块读取：calls=%d max=%d", calls, maxLen)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消后应正常退出：%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Tail 未退出")
	}
}

// TestTailSlowConsumerCatchUpUsesChunks 消费回调阻塞期间日志继续增长；
// 恢复后按固定块追赶，仍能读到末尾，且单次读取不超过 chunk。
func TestTailSlowConsumerCatchUpUsesChunks(t *testing.T) {
	day := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := writeLog(t, dir, day, jsonLine("旧行"))
	src := NewSource(dir, func() time.Time { return day })
	src.poll = 10 * time.Millisecond
	reads := &tailReads{}
	src.readChunk = reads.read

	first := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	last := make(chan Entry, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- src.Tail(ctx, func(e Entry) error {
			once.Do(func() {
				close(first)
				<-release
			})
			if e.Msg == "末尾" {
				select {
				case last <- e:
				default:
				}
			}
			return nil
		})
	}()
	time.Sleep(30 * time.Millisecond)

	appendLog(t, path, jsonLine("首行")+"\n")
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("首行未触发回调")
	}

	// 慢消费期间积压约 1.6MiB，超过单轮块数预算，追赶必须分多轮完成。
	var backlog strings.Builder
	for i := 0; i < 100; i++ {
		backlog.WriteString(jsonLine(strings.Repeat("y", 16<<10)))
		backlog.WriteByte('\n')
	}
	backlog.WriteString(jsonLine("末尾"))
	backlog.WriteByte('\n')
	appendLog(t, path, backlog.String())
	close(release)

	select {
	case e := <-last:
		if e.Msg != "末尾" {
			t.Fatalf("末尾内容不符：%+v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("慢消费恢复后未追赶到末尾")
	}
	if calls, _, maxLen := reads.snapshot(); maxLen > tailChunkBytes || calls <= tailChunksPerTick {
		t.Fatalf("追赶应按固定小块分多轮读取：calls=%d max=%d", calls, maxLen)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消后应正常退出：%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Tail 未退出")
	}
}

// TestFeedLinesLineCapAndRecovery 直接覆盖 pending 上限边界与超限恢复。
func TestFeedLinesLineCapAndRecovery(t *testing.T) {
	var entries []Entry
	fn := func(e Entry) error {
		entries = append(entries, e)
		return nil
	}
	lastMsg := func() string {
		if len(entries) == 0 {
			return ""
		}
		return entries[len(entries)-1].Msg
	}

	// 正常半行跨 chunk 拼接。
	pending, skipping, err := feedLines([]byte(`{"ts":"2026-11-05T10:00:00.000Z","level":"info","msg":"跨`), nil, false, fn)
	if err != nil || skipping || len(pending) == 0 {
		t.Fatalf("第一段应留作 pending：pending=%d skipping=%v err=%v", len(pending), skipping, err)
	}
	pending, skipping, err = feedLines([]byte(`块","action":"watch"}`+"\n"), pending, skipping, fn)
	if err != nil || skipping || len(pending) != 0 || lastMsg() != "跨块" {
		t.Fatalf("跨块行未正确拼接：pending=%d skipping=%v msg=%q err=%v", len(pending), skipping, lastMsg(), err)
	}

	// 恰好达到上限的 pending 不应误判超限；遇到换行后清空。
	pending, skipping, err = feedLines(bytes.Repeat([]byte("x"), tailMaxLineBytes), nil, false, fn)
	if err != nil || skipping || len(pending) != tailMaxLineBytes {
		t.Fatalf("上限内 pending 不应跳过：pending=%d skipping=%v err=%v", len(pending), skipping, err)
	}
	pending, skipping, err = feedLines([]byte("\n"), pending, skipping, fn)
	if err != nil || skipping || len(pending) != 0 {
		t.Fatalf("上限内 pending 收尾失败：pending=%d skipping=%v err=%v", len(pending), skipping, err)
	}

	// 超限且当前块内就有换行：整行丢弃，后续行恢复。
	oversized := strings.Repeat("x", tailMaxLineBytes+1) + "\n" + jsonLine("恢复一") + "\n"
	pending, skipping, err = feedLines([]byte(oversized), nil, false, fn)
	if err != nil || skipping || len(pending) != 0 || lastMsg() != "恢复一" {
		t.Fatalf("块内超限行未恢复：pending=%d skipping=%v msg=%q err=%v", len(pending), skipping, lastMsg(), err)
	}

	// 超限且无换行：进入跳过态；下一个块遇到换行后恢复。
	pending, skipping, err = feedLines([]byte(strings.Repeat("x", tailMaxLineBytes+1)), nil, false, fn)
	if err != nil || !skipping || len(pending) != 0 {
		t.Fatalf("无换行超限应进入跳过态：pending=%d skipping=%v err=%v", len(pending), skipping, err)
	}
	pending, skipping, err = feedLines([]byte("尾巴\n"+jsonLine("恢复二")+"\n"), pending, skipping, fn)
	if err != nil || skipping || len(pending) != 0 || lastMsg() != "恢复二" {
		t.Fatalf("跳过态未恢复：pending=%d skipping=%v msg=%q err=%v", len(pending), skipping, lastMsg(), err)
	}
}
