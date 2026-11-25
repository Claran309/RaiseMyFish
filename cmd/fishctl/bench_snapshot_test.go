package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParsePrometheus(t *testing.T) {
	text := `# HELP fishd_events_pending 待处理
# TYPE fishd_events_pending gauge
fishd_events_pending 7
fishd_skip_total{reason="stale"} 2
fishd_skip_total{reason="rate"} 3
fishd_reply_latency_seconds{channel="live",quantile="0.5"} 1.25
`
	values := parsePrometheus(text)
	if values["fishd_events_pending"][""] != 7 {
		t.Fatalf("events=%v", values)
	}
	if values["fishd_skip_total"][`reason="stale"`] != 2 {
		t.Fatalf("skip=%v", values)
	}
	if values["fishd_reply_latency_seconds"][`channel="live",quantile="0.5"`] != 1.25 {
		t.Fatalf("latency=%v", values)
	}
}

func TestApplyMetrics(t *testing.T) {
	snapshot := BenchSnapshot{}
	applyMetrics(&snapshot, map[string]map[string]float64{
		"fishd_scheduler_active":         {"": 4},
		"fishd_lease_lost_total":         {"": 1},
		"fishd_duplicate_send_total":     {"": 9},
		"fishd_skip_total":               {`reason="stale"`: 2, `reason="rate"`: 3},
		"fishd_degrade_total":            {`reason="parse"`: 5},
		"fishd_goroutines":               {"": 42},
		"fishd_heap_bytes":               {"": 1024},
		"fishd_events_pending":           {"": 99},
		"fishd_oldest_event_age_seconds": {"": 12.5},
		"fishd_db_pool_in_use":           {"": 2},
	})
	if snapshot.SchedulerActive != 4 || snapshot.DuplicateSendTotal != 9 || snapshot.Goroutines != 42 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if snapshot.SkipReasons["stale"] != 2 || snapshot.SkipReasons["rate"] != 3 {
		t.Fatalf("skip=%+v", snapshot.SkipReasons)
	}
	if snapshot.DegradeReasons["parse"] != 5 {
		t.Fatalf("degrade=%+v", snapshot.DegradeReasons)
	}
}

func TestFetchMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fishd_events_pending 3\n"))
	}))
	defer srv.Close()
	values, err := fetchMetrics(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if values["fishd_events_pending"][""] != 3 {
		t.Fatalf("values=%v", values)
	}
}

func TestBenchSnapshotSingleLineJSON(t *testing.T) {
	var buf bytes.Buffer
	snapshot := BenchSnapshot{Database: "raisemyfish_bench", EventsPending: 1, SkipReasons: map[string]float64{}}
	if err := json.NewEncoder(&buf).Encode(snapshot); err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(buf.String()), "\n") != 0 {
		t.Fatalf("不是单行 JSON：%q", buf.String())
	}
}

func TestSplitPromLineRejectsMalformed(t *testing.T) {
	for _, line := range []string{"", "no-space", "{bad} 1", "metric{bad 1"} {
		if _, _, _, ok := splitPromLine(line); ok {
			t.Fatalf("畸形行被接受：%q", line)
		}
	}
}
