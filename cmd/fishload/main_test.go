package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func liveLine(id, at, text string) string {
	return `{"id":"` + id + `","channel":"live","dir":"in","session":"10000001","text":"` + text + `","at":"` + at + `"}`
}

func TestLoadEventsSortsByAtAndValidates(t *testing.T) {
	path := writeJSONL(t,
		liveLine("live:10000001:2", "2026-11-24T10:00:02Z", "第二条"),
		liveLine("live:10000001:1", "2026-11-24T10:00:01Z", "第一条"),
	)
	events, err := loadEvents(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID != "live:10000001:1" {
		t.Fatalf("未按 at 排序：%+v", events)
	}
}

func TestLoadEventsRejectsInvalid(t *testing.T) {
	path := writeJSONL(t, `{"id":"bad","channel":"live","dir":"in","session":"1","text":"x","at":"2026-11-24T10:00:00Z"}`)
	if _, err := loadEvents(path, 0); err == nil {
		t.Fatal("非法事件被接受")
	}
}

func TestScheduleFixed(t *testing.T) {
	offsets := scheduleOffsets(4, options{Mode: "fixed", RatePerMin: 24})
	want := []time.Duration{0, 2500 * time.Millisecond, 5 * time.Second, 7500 * time.Millisecond}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("offset[%d]=%s want %s", i, offsets[i], want[i])
		}
	}
}

func TestScheduleBurst(t *testing.T) {
	opts := options{Mode: "burst", BurstCount: 100, BurstWindow: 10 * time.Second, BurstGap: 50 * time.Second}
	offsets := scheduleOffsets(101, opts)
	if offsets[0] != 0 || offsets[99] != 9900*time.Millisecond {
		t.Fatalf("首轮突发不正确：%s %s", offsets[0], offsets[99])
	}
	if offsets[100] != 60*time.Second {
		t.Fatalf("第二轮起点=%s want 1m", offsets[100])
	}
}

type fakePublisher struct {
	seen map[string]bool
	err  error
	got  []event.Event
}

func (p *fakePublisher) Publish(_ context.Context, ev event.Event) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	if p.seen[ev.ID] {
		return false, nil
	}
	p.seen[ev.ID] = true
	p.got = append(p.got, ev)
	return true, nil
}

func TestReplayCountsAndRecords(t *testing.T) {
	events := []event.Event{
		{ID: "live:1:1", Channel: event.ChLive, Dir: event.DirIn, Session: "1", Text: "a", At: time.Unix(1, 0)},
		{ID: "live:1:2", Channel: event.ChLive, Dir: event.DirIn, Session: "1", Text: "b", At: time.Unix(2, 0)},
	}
	pub := &fakePublisher{seen: map[string]bool{"live:1:1": true}}
	var record bytes.Buffer
	clock := time.Unix(100, 0)
	summary, err := replay(context.Background(), events, options{Mode: "fixed", RatePerMin: 6000},
		pub, &record, func() time.Time { return clock }, func(_ context.Context, d time.Duration) error {
			clock = clock.Add(d)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Published != 1 || summary.Duplicates != 1 || summary.Failed != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	lines := strings.Split(strings.TrimSpace(record.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("记录行数=%d", len(lines))
	}
	var first injectionRecord
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if !first.Duplicate || first.EventID != "live:1:1" {
		t.Fatalf("首条记录=%+v", first)
	}
}

// TestReplaySleepsRelativeIntervals 防止把绝对 offset 当增量重复睡眠。
func TestReplaySleepsRelativeIntervals(t *testing.T) {
	events := []event.Event{
		{ID: "live:1:1", Channel: event.ChLive, Dir: event.DirIn, Session: "1", Text: "a", At: time.Unix(1, 0)},
		{ID: "live:1:2", Channel: event.ChLive, Dir: event.DirIn, Session: "1", Text: "b", At: time.Unix(2, 0)},
		{ID: "live:1:3", Channel: event.ChLive, Dir: event.DirIn, Session: "1", Text: "c", At: time.Unix(3, 0)},
	}
	var slept []time.Duration
	clock := time.Unix(100, 0)
	_, err := replay(context.Background(), events, options{Mode: "fixed", RatePerMin: 24},
		&fakePublisher{seen: map[string]bool{}}, nil, func() time.Time { return clock },
		func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			clock = clock.Add(d)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{2500 * time.Millisecond, 2500 * time.Millisecond}
	if len(slept) != len(want) {
		t.Fatalf("sleep 次数=%d want %d (%v)", len(slept), len(want), slept)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Fatalf("sleep[%d]=%s want %s", i, slept[i], want[i])
		}
	}
}

func TestReplayStopsOnPublisherError(t *testing.T) {
	events := []event.Event{{ID: "live:1:1", Channel: event.ChLive, Dir: event.DirIn, Session: "1", Text: "a", At: time.Unix(1, 0)}}
	summary, err := replay(context.Background(), events, options{Mode: "fixed", RatePerMin: 6000},
		&fakePublisher{seen: map[string]bool{}, err: errors.New("boom")}, nil,
		time.Now, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if summary.Failed != 1 || summary.Published != 0 {
		t.Fatalf("summary=%+v", summary)
	}
}

// TestReplayRewriteAtUsesInjectionTime 压测口径：事件 at 应贴近实际注入时间。
func TestReplayRewriteAtUsesInjectionTime(t *testing.T) {
	fixed := time.Unix(12345, 0)
	events := []event.Event{{ID: "live:1:1", Channel: event.ChLive, Dir: event.DirIn, Session: "1", Text: "a", At: time.Unix(1, 0)}}
	pub := &fakePublisher{seen: map[string]bool{}}
	_, err := replay(context.Background(), events, options{Mode: "fixed", RatePerMin: 6000, RewriteAt: true},
		pub, nil, func() time.Time { return fixed }, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(pub.got) != 1 || !pub.got[0].At.Equal(fixed) {
		t.Fatalf("At=%s want %s", pub.got[0].At, fixed)
	}
}

func TestParseOptionsRejectsBadMode(t *testing.T) {
	_, err := parseOptions([]string{"--file", "x.jsonl", "--mode", "nope"}, os.Stderr)
	if err == nil {
		t.Fatal("非法 mode 被接受")
	}
}
