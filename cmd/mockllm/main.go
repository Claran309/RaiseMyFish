// mockllm 是压测用的 OpenAI 兼容 mock 服务。
//
// 只实现 /v1/chat/completions：
//   - 固定延迟与固定 token 用量，保证可复现；
//   - 可注入错误率与超时率（用于故障场景）；
//   - 自动识别 ReAct 协议提示词并返回合法 JSON 步骤，避免压测时降级；
//   - /stats 返回请求计数，便于压测脚本对账。
//
// 不读取模型密钥、不访问外网、不记录 prompt 正文。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config 是 mock 服务参数。
type Config struct {
	Latency          time.Duration
	PromptTokens     int
	CompletionTokens int
	Reply            string
	ErrorRate        float64
	TimeoutRate      float64
	TimeoutDelay     time.Duration
	Seed             int64
}

type server struct {
	cfg      Config
	mu       sync.Mutex
	rnd      *rand.Rand
	requests atomic.Int64
	errors   atomic.Int64
	timeouts atomic.Int64
	active   atomic.Int64
	peak     atomic.Int64
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "监听地址（只允许回环）")
	latency := flag.Duration("latency", 150*time.Millisecond, "固定响应延迟")
	promptTokens := flag.Int("prompt-tokens", 800, "上报的 prompt_tokens")
	completionTokens := flag.Int("completion-tokens", 20, "上报的 completion_tokens")
	reply := flag.String("reply", "收到，压测回复。", "普通回复正文")
	errorRate := flag.Float64("error-rate", 0, "HTTP 500 注入概率（0-1）")
	timeoutRate := flag.Float64("timeout-rate", 0, "超时注入概率（0-1）")
	timeoutDelay := flag.Duration("timeout-delay", 30*time.Second, "超时注入时的睡眠时长")
	seed := flag.Int64("seed", 1, "随机种子")
	flag.Parse()

	cfg := Config{
		Latency: *latency, PromptTokens: *promptTokens, CompletionTokens: *completionTokens,
		Reply: *reply, ErrorRate: *errorRate, TimeoutRate: *timeoutRate, TimeoutDelay: *timeoutDelay, Seed: *seed,
	}
	if err := validateAddr(*addr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	srv, err := newServer(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("mockllm listening on http://%s（latency=%s error_rate=%.2f timeout_rate=%.2f）\n",
		*addr, cfg.Latency, cfg.ErrorRate, cfg.TimeoutRate)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func validateAddr(addr string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return errors.New("addr 必须形如 127.0.0.1:18080")
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return errors.New("addr 端口无效")
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("mockllm 只允许监听回环地址")
	}
	return nil
}

func newServer(cfg Config) (*server, error) {
	if cfg.Latency < 0 || cfg.Latency > 10*time.Second {
		return nil, errors.New("latency 需在0至10秒")
	}
	if cfg.PromptTokens < 0 || cfg.CompletionTokens < 0 {
		return nil, errors.New("token 数不能为负")
	}
	if cfg.ErrorRate < 0 || cfg.ErrorRate > 1 || cfg.TimeoutRate < 0 || cfg.TimeoutRate > 1 {
		return nil, errors.New("error_rate/timeout_rate 需在0至1")
	}
	if strings.TrimSpace(cfg.Reply) == "" {
		cfg.Reply = "收到，压测回复。"
	}
	return &server{cfg: cfg, rnd: rand.New(rand.NewSource(cfg.Seed))}, nil
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.chat)
	mux.HandleFunc("/stats", s.stats)
	return mux
}

type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "读取请求失败", http.StatusBadRequest)
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Model) == "" || len(req.Messages) == 0 {
		http.Error(w, `{"error":{"message":"invalid request"}}`, http.StatusBadRequest)
		return
	}
	s.requests.Add(1)
	current := s.active.Add(1)
	for {
		old := s.peak.Load()
		if current <= old || s.peak.CompareAndSwap(old, current) {
			break
		}
	}
	defer s.active.Add(-1)
	if s.chance(s.cfg.TimeoutRate) {
		s.timeouts.Add(1)
		select {
		case <-time.After(s.cfg.TimeoutDelay):
		case <-r.Context().Done():
		}
		return
	}
	if s.cfg.Latency > 0 {
		select {
		case <-time.After(s.cfg.Latency):
		case <-r.Context().Done():
			return
		}
	}
	if s.chance(s.cfg.ErrorRate) {
		s.errors.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"mock injected error"}}`))
		return
	}
	reply := s.replyFor(req)
	resp := map[string]any{
		"id":      "mock-" + fmt.Sprint(s.requests.Load()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": reply},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":         s.cfg.PromptTokens,
			"completion_tokens":     s.cfg.CompletionTokens,
			"total_tokens":          s.cfg.PromptTokens + s.cfg.CompletionTokens,
			"prompt_tokens_details": map[string]any{"cached_tokens": 0},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// chance 在并发下安全地抽取一次概率，rate<=0 时直接返回 false。
func (s *server) chance(rate float64) bool {
	if rate <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rnd.Float64() < rate
}

// replyFor 识别 ReAct 协议：要求 JSON 步骤时回合法 JSON，否则回普通文本。
func (s *server) replyFor(req chatRequest) string {
	protocol := false
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, "每一步只输出一个 JSON 对象") ||
			strings.Contains(msg.Content, `"action"`) {
			protocol = true
			break
		}
	}
	if !protocol {
		return s.cfg.Reply
	}
	step := map[string]any{"thought": "mock", "action": "final", "final": s.cfg.Reply}
	data, err := json.Marshal(step)
	if err != nil {
		return `{"thought":"mock","action":"final","final":"收到"}`
	}
	return string(data)
}

func (s *server) stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{
		"requests": s.requests.Load(),
		"errors":   s.errors.Load(),
		"timeouts": s.timeouts.Load(),
		"active":   s.active.Load(),
		"peak":     s.peak.Load(),
	})
}
