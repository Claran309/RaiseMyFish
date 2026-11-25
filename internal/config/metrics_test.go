package config

import "testing"

// TestMetricsValidateLoopbackOnly 关闭时不校验；开启时只允许回环。
func TestMetricsValidateLoopbackOnly(t *testing.T) {
	if err := (Metrics{Enabled: false, Addr: "0.0.0.0:9091"}).Validate(); err != nil {
		t.Fatalf("关闭时不应校验：%v", err)
	}
	for _, addr := range []string{"127.0.0.1:9091", "localhost:9091", "[::1]:9091"} {
		if err := (Metrics{Enabled: true, Addr: addr}).Validate(); err != nil {
			t.Fatalf("%s 应被接受：%v", addr, err)
		}
	}
	for _, addr := range []string{"", "0.0.0.0:9091", "192.168.1.2:9091", "127.0.0.1", "127.0.0.1:abc"} {
		if err := (Metrics{Enabled: true, Addr: addr}).Validate(); err == nil {
			t.Fatalf("%q 应被拒绝", addr)
		}
	}
}

// TestDefaultMetricsDisabled 默认关闭且只监听回环。
func TestDefaultMetricsDisabled(t *testing.T) {
	m := defaultMetrics()
	if m.Enabled || m.Addr != "127.0.0.1:9091" {
		t.Fatalf("默认值不符：%+v", m)
	}
}
