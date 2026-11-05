package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWebValidate 空地址合法；已配置地址必须是无用户信息/查询的 HTTP(S)。
func TestWebValidate(t *testing.T) {
	good := []string{"", "https://search.example.com/api", "http://127.0.0.1:8080/search", "http://localhost:9000"}
	for _, raw := range good {
		if err := (Web{SearchURL: raw}).Validate(); err != nil {
			t.Errorf("地址 %q 应合法：%v", raw, err)
		}
	}
	bad := []string{
		"ftp://search.example.com/api",
		"https://user:pass@search.example.com/api",
		"https://search.example.com/api?token=secret",
		"https://search.example.com/api#frag",
		"not a url",
		"http://192.168.1.10:8080/search", // 非本机HTTP需要HTTPS
	}
	for _, raw := range bad {
		if err := (Web{SearchURL: raw}).Validate(); err == nil {
			t.Errorf("地址 %q 应被拒绝", raw)
		}
	}
	if err := (Web{SearchKey: "line1\nline2"}).Validate(); err == nil {
		t.Error("带换行的密钥应被拒绝")
	}
}

// TestWebProtocolAndAllowedDomains 协议枚举与域名白名单格式。
func TestWebProtocolAndAllowedDomains(t *testing.T) {
	good := []Web{
		{Protocol: ""},
		{Protocol: "generic"},
		{Protocol: "serper"},
		{Protocol: "BING"},
		{AllowedDomains: []string{"example.com", "sub.bilibili.com", "127.0.0.1"}},
	}
	for _, w := range good {
		if err := w.Validate(); err != nil {
			t.Errorf("配置 %+v 应合法：%v", w, err)
		}
	}
	bad := []Web{
		{Protocol: "google"},
		{AllowedDomains: []string{""}},
		{AllowedDomains: []string{"https://example.com"}},
		{AllowedDomains: []string{"*.example.com"}},
		{AllowedDomains: []string{"example.com:443"}},
		{AllowedDomains: []string{"-bad.example.com"}},
		{AllowedDomains: []string{"exa mple.com"}},
	}
	for _, w := range bad {
		if err := w.Validate(); err == nil {
			t.Errorf("配置 %+v 应被拒绝", w)
		}
	}
}

// TestLoadWebProtocolAndDomains 新字段能从 TOML 加载。
func TestLoadWebProtocolAndDomains(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private@localhost/fish")
	p := filepath.Join(t.TempDir(), "fishd.toml")
	body := "[web]\nsearch_url='https://api.serper.dev/search'\nprotocol='serper'\nallowed_domains=['bilibili.com','zhihu.com']\n"
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.Web.Protocol != "serper" || len(cfg.Web.AllowedDomains) != 2 || cfg.Web.AllowedDomains[0] != "bilibili.com" {
		t.Fatalf("新字段未加载：%+v", cfg.Web)
	}
}

// TestLoadWebConfigAndKeyNotSerialized 配置能加载，密钥不进入 JSON。
func TestLoadWebConfigAndKeyNotSerialized(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private@localhost/fish")
	p := filepath.Join(t.TempDir(), "fishd.toml")
	body := "[web]\nsearch_url='https://search.example.com/api'\nsearch_key='super-secret'\n"
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.Web.SearchURL != "https://search.example.com/api" || cfg.Web.SearchKey != "super-secret" {
		t.Fatalf("配置未正确加载：%+v", cfg.Web)
	}
	data, err := json.Marshal(cfg.Web)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret") {
		t.Fatalf("密钥被序列化：%s", data)
	}
}

// TestLoadWebRejectsUnknownFieldAndBadURL 严格 TOML 与地址校验在加载期生效。
func TestLoadWebRejectsUnknownFieldAndBadURL(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private@localhost/fish")
	cases := []string{
		"[web]\nsearch_url='https://search.example.com'\nunknown='private-value'",
		"[web]\nsearch_url='ftp://search.example.com'",
	}
	for _, body := range cases {
		p := filepath.Join(t.TempDir(), "fishd.toml")
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(p, "")
		if err == nil || strings.Contains(err.Error(), "private-value") {
			t.Errorf("应拒绝且不得回显敏感值：%v", err)
		}
	}
}
