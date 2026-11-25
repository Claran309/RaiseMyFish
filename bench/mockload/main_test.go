package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunConcurrent(t *testing.T) {
	var active, peak atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stats" {
			_, _ = w.Write([]byte(`{"peak":5}`))
			return
		}
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()
	result, err := run(context.Background(), srv.URL, 10, 20, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK != 20 || result.Errors != 0 || result.Peak != 5 || result.P50MS < 0 {
		t.Fatalf("result=%+v", result)
	}
}
