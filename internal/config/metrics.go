package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Metrics 是 Prometheus 指标端点（`[metrics]`）。
//
// 默认关闭：关闭时 fishd 不注册路由、不创建 goroutine，行为与现在完全一致。
// 只允许监听回环，避免把运行时指标暴露到公网。
type Metrics struct {
	Enabled bool   `toml:"enabled"`
	Addr    string `toml:"addr"`
}

func defaultMetrics() Metrics {
	return Metrics{Enabled: false, Addr: "127.0.0.1:9091"}
}

// Validate 只允许回环地址。
func (m Metrics) Validate() error {
	if !m.Enabled {
		return nil
	}
	addr := strings.TrimSpace(m.Addr)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("metrics.addr 必须形如 127.0.0.1:9091")
	}
	if _, err := strconv.Atoi(port); err != nil {
		return errors.New("metrics.addr 端口必须是数字")
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("metrics 只允许监听回环地址（127.0.0.1 或 ::1）")
	}
	return nil
}
