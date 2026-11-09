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
	good := []string{"", "https://search.example.com/api", "http://127.0.0.1:8080/search", "http://localhost:9000",
		"http://host.docker.internal:9200/search", "http://HOST.DOCKER.INTERNAL:9200/search"}
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
		// 容器宿主别名只认精确的 host.docker.internal；相似域名/网关 IP 不放行。
		"http://host.containers.internal:9200/search",
		"http://gateway.docker.internal:9200/search",
		"http://host.docker.internal.evil.com:9200/search",
		"http://host.docker.internal.:9200/search",
		"http://192.168.65.254:9200/search",
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

// TestLoadWebSearchContainerHost 完整加载接受容器宿主别名（N19）。
func TestLoadWebSearchContainerHost(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private@localhost/fish")
	p := filepath.Join(t.TempDir(), "fishd.toml")
	body := "[web]\nsearch_url='http://host.docker.internal:9200/search'\n"
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.Web.SearchURL != "http://host.docker.internal:9200/search" {
		t.Fatalf("search_url 未生效：%q", cfg.Web.SearchURL)
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

// unsetForTest 确保进程环境里没有该变量（t.Setenv 负责测试后恢复原值）。
func unsetForTest(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

// TestWebResolveKeyFromDotenv O07 核心回归：dotenv 里的同名 key 必须生效。
//
// 旧实现里 ResolveKey 直接 os.Getenv，只看进程环境，
// 结果 .env 配了 search_key_env 也不生效——配置分裂成两套来源。
func TestWebResolveKeyFromDotenv(t *testing.T) {
	const key = "FISH_TEST_WEB_KEY_DOTENV"
	unsetForTest(t, key)

	dir := t.TempDir()
	path := filepath.Join(dir, "fishd.toml")
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("[web]\nsearch_key_env='"+key+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := key + "=from-dotenv\nFISH_DB_DSN=postgres://fish:pw@localhost/fish\n"
	if err := os.WriteFile(envPath, []byte(env), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path, envPath)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if got := cfg.Web.ResolveKey(); got != "from-dotenv" {
		t.Fatalf("dotenv 中的 key 未生效：%q", got)
	}
	// 只读解析：不能把 dotenv 的值塞进进程环境。
	if os.Getenv(key) != "" {
		t.Fatal("加载不应改进程环境")
	}
}

// TestWebResolveKeyProcessEnvWins 进程环境优先于 dotenv（与主加载器一致）。
func TestWebResolveKeyProcessEnvWins(t *testing.T) {
	const key = "FISH_TEST_WEB_KEY_ENV"
	t.Setenv(key, "from-process")
	t.Setenv("FISH_DB_DSN", "postgres://fish:pw@localhost/fish")

	dir := t.TempDir()
	path := filepath.Join(dir, "fishd.toml")
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("[web]\nsearch_key_env='"+key+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte(key+"=from-dotenv\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path, envPath)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if got := cfg.Web.ResolveKey(); got != "from-process" {
		t.Fatalf("进程环境应优先：%q", got)
	}
}

// TestWebResolveKeyLiteralFallback 环境里没有该变量时，退回字面值（兼容旧配置）。
func TestWebResolveKeyLiteralFallback(t *testing.T) {
	const key = "FISH_TEST_WEB_KEY_ABSENT"
	unsetForTest(t, key)
	t.Setenv("FISH_DB_DSN", "postgres://fish:pw@localhost/fish")

	p := filepath.Join(t.TempDir(), "fishd.toml")
	body := "[web]\nsearch_key='literal-key'\nsearch_key_env='" + key + "'\n"
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(p, "")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if got := cfg.Web.ResolveKey(); got != "literal-key" {
		t.Fatalf("无环境变量时应退回字面值：%q", got)
	}
}

// TestWebResolveKeyExplicitEmptyEnvWins 显式空环境覆盖字面值（与 Embedding/LLM 同一语义）。
func TestWebResolveKeyExplicitEmptyEnvWins(t *testing.T) {
	const key = "FISH_TEST_WEB_KEY_EMPTY"
	t.Setenv(key, "")
	t.Setenv("FISH_DB_DSN", "postgres://fish:pw@localhost/fish")

	p := filepath.Join(t.TempDir(), "fishd.toml")
	body := "[web]\nsearch_key='literal-key'\nsearch_key_env='" + key + "'\n"
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(p, "")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if got := cfg.Web.ResolveKey(); got != "" {
		t.Fatalf("显式空环境应关闭 key，实际 %q", got)
	}
}

// TestWebResolveKeyWithoutLoad 未经 Load 的对象只认字面值，不再偷偷读进程环境。
func TestWebResolveKeyWithoutLoad(t *testing.T) {
	const key = "FISH_TEST_WEB_KEY_MANUAL"
	t.Setenv(key, "from-process")
	manual := Web{SearchKey: "literal", SearchKeyEnv: key}
	if got := manual.ResolveKey(); got != "literal" {
		t.Fatalf("未 Load 的对象应只认字面值：%q", got)
	}
}
