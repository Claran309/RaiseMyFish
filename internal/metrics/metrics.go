// Package metrics 是零依赖的 Prometheus 文本指标实现。
//
// 目标：在不引入 prometheus/client_golang 的前提下暴露 /metrics。
// 只支持 counter/gauge 与字符串标签，足够本项目压测观测；
// 不实现 histogram/summary——分位由 SQL 计算后以 gauge 输出。
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Registry 是进程内指标集合；并发安全。
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
}

type family struct {
	name   string
	help   string
	typ    string
	labels []string
	values map[string]float64
	order  []string
}

// New 构造空注册表。
func New() *Registry {
	return &Registry{families: make(map[string]*family)}
}

// Counter 注册 counter；第一次调用决定 help 与标签名。
func (r *Registry) Counter(name, help string, labels ...string) {
	r.ensure(name, help, "counter", labels)
}

// Gauge 注册 gauge；第一次调用决定 help 与标签名。
func (r *Registry) Gauge(name, help string, labels ...string) {
	r.ensure(name, help, "gauge", labels)
}

func (r *Registry) ensure(name, help, typ string, labels []string) {
	if r == nil || name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.families[name]; ok {
		return
	}
	r.families[name] = &family{
		name: name, help: help, typ: typ, labels: labels,
		values: make(map[string]float64),
	}
}

// Has 报告指标是否已注册（测试与装配自检用）。
func (r *Registry) Has(name string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.families[name]
	return ok
}

// Inc 给 counter 加 1。
func (r *Registry) Inc(name string, labels map[string]string) {
	r.Add(name, labels, 1)
}

// Add 给 counter 加 delta；负值被忽略。
func (r *Registry) Add(name string, labels map[string]string, delta float64) {
	if r == nil || delta <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.families[name]
	if f == nil || f.typ != "counter" {
		return
	}
	key := labelKey(f.labels, labels)
	if _, ok := f.values[key]; !ok {
		f.order = append(f.order, key)
	}
	f.values[key] += delta
}

// Set 设置 gauge 值。
func (r *Registry) Set(name string, labels map[string]string, value float64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.families[name]
	if f == nil || f.typ != "gauge" {
		return
	}
	key := labelKey(f.labels, labels)
	if _, ok := f.values[key]; !ok {
		f.order = append(f.order, key)
	}
	f.values[key] = value
}

// Handler 返回 Prometheus 文本格式的 HTTP handler。
//
// collect 在每次抓取前执行，用于填充 DB/运行时 gauge；失败只忽略，
// 不影响已注册指标输出。
func (r *Registry) Handler(collect func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if collect != nil {
			collect()
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(r.Render()))
	})
}

// Render 输出稳定的文本格式：家族按名字排序，标签值按 key 排序。
func (r *Registry) Render() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	names := make([]string, 0, len(r.families))
	for name := range r.families {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		f := r.families[name]
		if len(f.values) == 0 {
			continue
		}
		if f.help != "" {
			fmt.Fprintf(&b, "# HELP %s %s\n", f.name, f.help)
		}
		fmt.Fprintf(&b, "# TYPE %s %s\n", f.name, f.typ)
		keys := append([]string(nil), f.order...)
		sort.Strings(keys)
		for _, key := range keys {
			if len(f.labels) == 0 {
				fmt.Fprintf(&b, "%s %s\n", f.name, formatFloat(f.values[key]))
				continue
			}
			// %q 负责引号与反斜杠/换行转义，符合 Prometheus 文本格式。
			parts := strings.Split(key, "\x1f")
			rendered := make([]string, 0, len(f.labels))
			for i, name := range f.labels {
				value := ""
				if i < len(parts) {
					value = parts[i]
				}
				rendered = append(rendered, fmt.Sprintf("%s=%q", name, value))
			}
			fmt.Fprintf(&b, "%s{%s} %s\n", f.name, strings.Join(rendered, ","), formatFloat(f.values[key]))
		}
	}
	r.mu.Unlock()
	return b.String()
}

func labelKey(names []string, labels map[string]string) string {
	if len(names) == 0 {
		return ""
	}
	values := make([]string, len(names))
	for i, name := range names {
		values[i] = labels[name]
	}
	return strings.Join(values, "\x1f")
}

func formatFloat(v float64) string {
	return fmt.Sprintf("%g", v)
}
