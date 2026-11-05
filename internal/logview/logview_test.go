package logview

// 本文件测日志读取：白名单字段、脱敏、倒序过滤、SSE tail 的追加与半行拼接。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLog(t *testing.T, dir string, day time.Time, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "fishd-"+day.Format("2006-01-02")+".log")
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRecentFiltersAndOrders(t *testing.T) {
	day := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	writeLog(t, dir, day,
		`{"ts":"2026-11-05T10:00:00.000Z","level":"info","msg":"第一条","action":"watch","room":"1"}`,
		`{"ts":"2026-11-05T10:01:00.000Z","level":"info","msg":"主动弹幕已发送","action":"proactive","runes":8,"unknown":"丢弃"}`,
		`not json`,
		`{"ts":"2026-11-05T10:02:00.000Z","level":"warn","msg":"失败","action":"proactive","error":"timeout"}`,
	)
	src := NewSource(dir, func() time.Time { return day })

	got, err := src.Recent(Query{Actions: []string{"proactive"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应命中 2 条，实际 %d：%+v", len(got), got)
	}
	if got[0].Msg != "失败" || got[1].Msg != "主动弹幕已发送" {
		t.Errorf("顺序应为最新在前：%+v", got)
	}
	if _, exists := got[1].Fields["unknown"]; exists {
		t.Error("未知字段不应展示")
	}
	if got[1].Fields["runes"] != "8" {
		t.Errorf("白名单数值字段丢失：%+v", got[1].Fields)
	}

	// 字段 OR 过滤：只留 tool=gift。
	writeLog(t, dir, day,
		`{"ts":"2026-11-05T10:00:00.000Z","level":"info","msg":"顺手","action":"tool_side_effect","tool":"follow"}`,
		`{"ts":"2026-11-05T10:01:00.000Z","level":"info","msg":"送礼","action":"tool_side_effect","tool":"gift"}`,
	)
	got, err = src.Recent(Query{Actions: []string{"tool_side_effect"}, Matches: []map[string]string{{"tool": "gift"}}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Fields["tool"] != "gift" {
		t.Fatalf("字段过滤不符：%+v", got)
	}
}

func TestRecentRedactsSecrets(t *testing.T) {
	day := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	writeLog(t, dir, day,
		`{"ts":"2026-11-05T10:00:00.000Z","level":"info","msg":"cookie SESSDATA=canary; bili_jct=csrf","action":"watch","reason":"Authorization: Bearer canary-token"}`,
	)
	src := NewSource(dir, func() time.Time { return day })
	got, err := src.Recent(Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("应命中 1 条：%+v", got)
	}
	all := got[0].Msg + " " + got[0].Fields["reason"]
	if strings.Contains(all, "canary") || strings.Contains(all, "csrf") {
		t.Fatalf("未脱敏：%s", all)
	}
	if !strings.Contains(all, "SESSDATA=***") {
		t.Errorf("脱敏格式不符：%s", all)
	}
}

func TestRecentMissingAndYesterday(t *testing.T) {
	day := time.Date(2026, 11, 5, 0, 10, 0, 0, time.UTC)
	dir := t.TempDir()
	src := NewSource(dir, func() time.Time { return day })
	if got, err := src.Recent(Query{Limit: 5}); err != nil || len(got) != 0 {
		t.Fatalf("空目录应返回空：%v %+v", err, got)
	}

	writeLog(t, dir, day.AddDate(0, 0, -1),
		`{"ts":"2026-11-04T23:59:00.000Z","level":"info","msg":"昨天的","action":"watch"}`,
	)
	got, err := src.Recent(Query{Limit: 5})
	if err != nil || len(got) != 1 || got[0].Msg != "昨天的" {
		t.Fatalf("应补读昨天：%v %+v", err, got)
	}

	if _, err := NewSource("", nil).Recent(Query{}); err == nil {
		t.Error("空目录路径应报错")
	}
}

func TestTailAppendsAndJoinsPartialLines(t *testing.T) {
	day := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := writeLog(t, dir, day,
		`{"ts":"2026-11-05T10:00:00.000Z","level":"info","msg":"旧行","action":"watch"}`,
	)
	src := NewSource(dir, func() time.Time { return day })
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

	// 等待 Tail 完成首次定位（seek 到末尾），避免把旧行当成新行。
	time.Sleep(30 * time.Millisecond)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	half := `{"ts":"2026-11-05T10:01:00.000Z","level":"info","msg":"新`
	if _, err := file.WriteString(half); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		t.Fatalf("半行不应推送：%+v", e)
	case <-time.After(80 * time.Millisecond):
	}
	if _, err := file.WriteString(`行","action":"proactive"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		if e.Msg != "新行" || e.Action != "proactive" {
			t.Fatalf("推送内容不符：%+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("追加的日志未被推送")
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

func TestTailHandlesTruncate(t *testing.T) {
	day := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	writeLog(t, dir, day,
		`{"ts":"2026-11-05T10:00:00.000Z","level":"info","msg":"很长很长的一行","action":"watch"}`,
		`{"ts":"2026-11-05T10:00:30.000Z","level":"info","msg":"第二行","action":"watch"}`,
	)
	src := NewSource(dir, func() time.Time { return day })
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

	// 覆盖成更短的文件：应从头重读。
	writeLog(t, dir, day, `{"ts":"2026-11-05T10:02:00.000Z","level":"info","msg":"截断后","action":"watch"}`)
	select {
	case e := <-got:
		if e.Msg != "截断后" {
			t.Fatalf("截断后内容不符：%+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("截断后未重新读取")
	}
	cancel()
	<-done
}
