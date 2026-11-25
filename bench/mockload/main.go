// mockload 直接对 mockllm 打固定并发，用于 R4 的 10/50 并发基线。
// 不经过 fishd/调度器，避免把会话串行误当成模型并发能力。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

type sample struct {
	OK      int     `json:"ok"`
	Errors  int     `json:"errors"`
	P50MS   float64 `json:"p50_ms"`
	P90MS   float64 `json:"p90_ms"`
	P99MS   float64 `json:"p99_ms"`
	MaxMS   float64 `json:"max_ms"`
	Elapsed float64 `json:"elapsed_ms"`
	Peak    int64   `json:"mock_peak"`
}

func main() {
	url := flag.String("url", "http://127.0.0.1:18081", "mockllm base URL")
	concurrency := flag.Int("concurrency", 10, "并发请求数")
	requests := flag.Int("requests", 20, "总请求数")
	timeout := flag.Duration("timeout", 30*time.Second, "单请求超时")
	flag.Parse()
	if *concurrency < 1 || *requests < *concurrency {
		fmt.Fprintln(os.Stderr, "concurrency 需 >=1 且 requests >= concurrency")
		os.Exit(1)
	}
	result, err := run(context.Background(), *url, *concurrency, *requests, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, _ := json.Marshal(result)
	fmt.Println(string(data))
}

func run(ctx context.Context, base string, concurrency, requests int, timeout time.Duration) (sample, error) {
	client := &http.Client{Timeout: timeout}
	body := []byte(`{"model":"mock","messages":[{"role":"user","content":"R4 并发基线"}]}`)
	start := time.Now()
	lat := make([]float64, 0, requests)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errCount := 0
	jobs := make(chan struct{}, requests)
	for i := 0; i < requests; i++ {
		jobs <- struct{}{}
	}
	close(jobs)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				started := time.Now()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
				if err != nil {
					mu.Lock()
					errCount++
					mu.Unlock()
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					mu.Lock()
					errCount++
					mu.Unlock()
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					mu.Lock()
					errCount++
					mu.Unlock()
					continue
				}
				mu.Lock()
				lat = append(lat, float64(time.Since(started).Milliseconds()))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sort.Float64s(lat)
	result := sample{OK: len(lat), Errors: errCount, Elapsed: float64(time.Since(start).Milliseconds())}
	result.P50MS = percentile(lat, 0.50)
	result.P90MS = percentile(lat, 0.90)
	result.P99MS = percentile(lat, 0.99)
	if len(lat) > 0 {
		result.MaxMS = lat[len(lat)-1]
	}
	var stats struct {
		Peak int64 `json:"peak"`
	}
	if err := getJSON(ctx, client, base+"/stats", &stats); err == nil {
		result.Peak = stats.Peak
	}
	return result, nil
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * q)
	return sorted[idx]
}

func getJSON(ctx context.Context, client *http.Client, url string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(target)
}
