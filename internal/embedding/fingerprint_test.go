package embedding

// 本文件测索引指纹：稳定、对向量空间参数敏感、不含凭据。

import "testing"

// TestIndexFingerprintStable 相同参数必须得到相同指纹。
func TestIndexFingerprintStable(t *testing.T) {
	a := IndexFingerprint("https://api.example.com/v1/embeddings", "text-embedding-3-small", 1024, "memory.title-summary/v1")
	b := IndexFingerprint("https://api.example.com/v1/embeddings", "text-embedding-3-small", 1024, "memory.title-summary/v1")
	if a == "" || a != b {
		t.Fatalf("指纹不稳定：%q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("指纹应为 64 位十六进制，实际 %q", a)
	}
	// 前后空白不应改变指纹。
	if got := IndexFingerprint("  https://api.example.com/v1/embeddings ", " text-embedding-3-small ", 1024, " memory.title-summary/v1 "); got != a {
		t.Errorf("空白处理不一致：%q vs %q", got, a)
	}
}

// TestIndexFingerprintSensitivity 提供方/模型/维度/模板任一变化都必须换指纹。
func TestIndexFingerprintSensitivity(t *testing.T) {
	base := IndexFingerprint("https://api.example.com/v1/embeddings", "model-a", 1024, "template/v1")
	cases := map[string]string{
		"换提供方": IndexFingerprint("https://api.other.com/v1/embeddings", "model-a", 1024, "template/v1"),
		"换模型":  IndexFingerprint("https://api.example.com/v1/embeddings", "model-b", 1024, "template/v1"),
		"换维度":  IndexFingerprint("https://api.example.com/v1/embeddings", "model-a", 768, "template/v1"),
		"换模板":  IndexFingerprint("https://api.example.com/v1/embeddings", "model-a", 1024, "template/v2"),
	}
	seen := map[string]string{base: "基准"}
	for name, got := range cases {
		if got == base {
			t.Errorf("%s 未改变指纹：%q", name, got)
		}
		if prev, ok := seen[got]; ok {
			t.Errorf("%s 与 %s 指纹相同：%q", name, prev, got)
		}
		seen[got] = name
	}
}

// TestClientFingerprintUsesConfig 客户端指纹取自实际配置，且与 API Key 无关。
func TestClientFingerprintUsesConfig(t *testing.T) {
	newClient := func(key string) *Client {
		client, err := New(Options{
			URL:       "http://127.0.0.1:8077/v1/embeddings",
			Model:     "bge-m3",
			APIKey:    key,
			Dimension: 1024,
		})
		if err != nil {
			t.Fatalf("构造客户端失败：%v", err)
		}
		return client
	}
	want := IndexFingerprint("http://127.0.0.1:8077/v1/embeddings", "bge-m3", 1024, "memory.title-summary/v1")
	if got := newClient("key-a").Fingerprint("memory.title-summary/v1"); got != want {
		t.Errorf("客户端指纹与配置不符：%q vs %q", got, want)
	}
	if got := newClient("key-b").Fingerprint("memory.title-summary/v1"); got != want {
		t.Errorf("换密钥不应改变指纹：%q vs %q", got, want)
	}
	var nilClient *Client
	if got := nilClient.Fingerprint("template/v1"); got != "" {
		t.Errorf("nil 客户端应返回空指纹：%q", got)
	}
}
