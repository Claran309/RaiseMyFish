package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func loadGiftConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gift.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path, "")
}

func TestGiftDefaultsOffAndDryRun(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=canary")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gift.Enabled || cfg.Gift.Real {
		t.Fatal("送礼必须默认关闭且dry-run")
	}
	if cfg.Gift.GiftID != "31036" || cfg.Gift.Num != 1 || cfg.Gift.DailyLimit != 1 {
		t.Fatalf("送礼默认值不符：%+v", cfg.Gift)
	}
	if cfg.Gift.MaxPrice != 100 || cfg.Gift.MaxNum != 10 || len(cfg.Gift.AllowedRooms) != 0 {
		t.Fatalf("送礼限制默认值不符：%+v", cfg.Gift)
	}
	if cfg.Gift.NodePath != "node" || cfg.Gift.ScriptPath != "tools/bili-whale/gift.mjs" || cfg.Gift.TimeoutMS != 30000 {
		t.Fatalf("送礼运行默认值不符：%+v", cfg.Gift)
	}
	if cfg.Gift.Cookie != "" || cfg.Gift.CookieFile != "" {
		t.Fatal("未启用时不应解析凭据")
	}
}

func TestGiftLimitsAndWhitelistLoad(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=canary")
	cfg, err := loadGiftConfig(t, "[gift]\nenabled=true\nmax_price=500\nmax_num=5\nnum=5\nallowed_rooms=[\"123\", \"456\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gift.MaxPrice != 500 || cfg.Gift.MaxNum != 5 || cfg.Gift.Num != 5 {
		t.Fatalf("送礼限制未按配置加载：%+v", cfg.Gift)
	}
	if !slices.Equal(cfg.Gift.AllowedRooms, []string{"123", "456"}) {
		t.Fatalf("房间白名单未按配置加载：%+v", cfg.Gift.AllowedRooms)
	}
	if cfg.Gift.Cookie != "" || cfg.Gift.CookieFile != "" {
		t.Fatal("dry-run不应解析凭据")
	}
}

func TestGiftDryRunDoesNotReadCredentials(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=canary")
	cfg, err := loadGiftConfig(t, "[gift]\nenabled=true\nreal=false\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gift.Cookie != "" || cfg.Gift.CookieFile != "" {
		t.Fatal("dry-run不应解析凭据")
	}
	data, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), "canary") {
		t.Fatal("序列化泄露凭据")
	}
}

func TestGiftRealResolvesCredentialsPrivately(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=canary")
	cfg, err := loadGiftConfig(t, "[gift]\nenabled=true\nreal=true\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gift.Cookie != "SESSDATA=canary" {
		t.Fatal("未从环境解析B站凭据")
	}
	data, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), "canary") {
		t.Fatal("序列化泄露凭据")
	}

	t.Setenv("FISH_BILI_COOKIE", "")
	t.Setenv("FISH_BILI_COOKIE_FILE", "C:/private/cookie.txt")
	cfg, err = loadGiftConfig(t, "[gift]\nenabled=true\nreal=true\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gift.CookieFile != "C:/private/cookie.txt" || cfg.Gift.Cookie != "" {
		t.Fatal("cookie文件回退失败")
	}
	data, err = json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), "private") {
		t.Fatal("序列化泄露凭据文件路径")
	}
}

func TestGiftRealRequiresCredentials(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	t.Setenv("FISH_BILI_COOKIE", "")
	t.Setenv("FISH_BILI_COOKIE_FILE", "")
	if _, err := loadGiftConfig(t, "[gift]\nenabled=true\nreal=true\n"); err == nil {
		t.Fatal("真实送礼缺少凭据未被拒绝")
	}
}

func TestGiftInvalidConfigIsSafe(t *testing.T) {
	cases := []struct {
		name string
		body string
		env  func(*testing.T)
	}{
		{"礼物ID为零", "[gift]\nenabled=true\ngift_id=\"0\"\n", nil},
		{"礼物ID非数字", "[gift]\nenabled=true\ngift_id=\"abc\"\n", nil},
		{"礼物ID超长", "[gift]\nenabled=true\ngift_id=\"99999999999999999\"\n", nil},
		{"数量为零", "[gift]\nenabled=true\nnum=0\n", nil},
		{"数量超限", "[gift]\nenabled=true\nnum=101\n", nil},
		{"数量超过上限", "[gift]\nenabled=true\nnum=11\n", nil},
		{"价格上限为零", "[gift]\nenabled=true\nmax_price=0\n", nil},
		{"价格上限超限", "[gift]\nenabled=true\nmax_price=10001\n", nil},
		{"数量上限为零", "[gift]\nenabled=true\nmax_num=0\n", nil},
		{"数量上限超限", "[gift]\nenabled=true\nmax_num=101\n", nil},
		{"白名单非数字", "[gift]\nenabled=true\nallowed_rooms=[\"abc\"]\n", nil},
		{"白名单为零", "[gift]\nenabled=true\nallowed_rooms=[\"0\"]\n", nil},
		{"白名单前导零", "[gift]\nenabled=true\nallowed_rooms=[\"00123\"]\n", nil},
		{"白名单含空白", "[gift]\nenabled=true\nallowed_rooms=[\" 123\"]\n", nil},
		{"白名单超长", "[gift]\nenabled=true\nallowed_rooms=[\"99999999999999999\"]\n", nil},
		{"限额为零", "[gift]\nenabled=true\ndaily_limit=0\n", nil},
		{"限额超限", "[gift]\nenabled=true\ndaily_limit=101\n", nil},
		{"超时过短", "[gift]\nenabled=true\ntimeout_ms=999\n", nil},
		{"超时过长", "[gift]\nenabled=true\ntimeout_ms=600001\n", nil},
		{"node为空", "[gift]\nenabled=true\nnode_path=\" \"\n", nil},
		{"脚本含换行", "[gift]\nenabled=true\nscript_path=\"a\\nb\"\n", nil},
		{"真送数量超限额", "[gift]\nenabled=true\nreal=true\nnum=2\ndaily_limit=1\n", func(t *testing.T) {
			t.Setenv("FISH_BILI_COOKIE", "SESSDATA=canary")
		}},
		{"凭据含控制字符", "[gift]\nenabled=true\nreal=true\n", func(t *testing.T) {
			t.Setenv("FISH_BILI_COOKIE", "SESSDATA=canary\n\r")
		}},
		{"未知字段", "[gift]\nenabled=true\nfoo=1\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FISH_DB_DSN", "postgres://fixture")
			t.Setenv("FISH_BILI_COOKIE", "")
			t.Setenv("FISH_BILI_COOKIE_FILE", "")
			if tc.env != nil {
				tc.env(t)
			}
			_, err := loadGiftConfig(t, tc.body)
			if err == nil {
				t.Fatal("无效送礼配置未被拒绝")
			}
			if strings.Contains(err.Error(), "canary") {
				t.Fatal("错误泄露凭据")
			}
		})
	}
}

func TestGiftDisabledIgnoresRuntimeFields(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=canary")
	cfg, err := loadGiftConfig(t, "[gift]\nenabled=false\nreal=true\nnum=101\n")
	if err != nil {
		t.Fatalf("关闭状态不应校验运行时字段：%v", err)
	}
	if cfg.Gift.Cookie != "" {
		t.Fatal("关闭状态不应解析凭据")
	}
}
