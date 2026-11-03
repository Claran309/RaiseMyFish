package bili

// 本文件测纯 Go 通道复用的凭据读取：显式值优先、原始串/JSON 文件、错误不泄露路径与内容。

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCredential(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCookieExplicitWins(t *testing.T) {
	got, err := LoadCookie("SESSDATA=explicit; bili_jct=csrf", "not-a-real-file")
	if err != nil || got != "SESSDATA=explicit; bili_jct=csrf" {
		t.Fatalf("显式凭据应优先：%q %v", got, err)
	}
	if CSRF := CSRFToken(got); CSRF != "csrf" {
		t.Errorf("csrf = %q", CSRF)
	}
}

func TestLoadCookieRawFile(t *testing.T) {
	path := writeCredential(t, "SESSDATA=file-session; bili_jct=file-csrf\n")
	got, err := LoadCookie("", path)
	if err != nil || got != "SESSDATA=file-session; bili_jct=file-csrf" {
		t.Fatalf("原始串文件解析失败：%q %v", got, err)
	}
}

func TestLoadCookieJSONFile(t *testing.T) {
	path := writeCredential(t, `{"sessdata":"s1","bili_jct":"c1","buvid3":"b1"}`)
	got, err := LoadCookie("", path)
	if err != nil || got != "SESSDATA=s1; bili_jct=c1; buvid3=b1" {
		t.Fatalf("JSON 文件解析失败：%q %v", got, err)
	}
}

func TestLoadCookieRejectsSafely(t *testing.T) {
	cases := []string{
		"",                       // 空文件
		"bili_jct=csrf",          // 缺 SESSDATA
		"SESSDATA=a\r\nX-Bad: 1", // 控制字符
		`{"bili_jct":"csrf"}`,    // JSON 缺 sessdata
		`{"sessdata":"a;b"}`,     // 分隔符注入
		`{"sessdata":123}`,       // 类型错误
	}
	for _, body := range cases {
		path := writeCredential(t, body)
		_, err := LoadCookie("", path)
		if err == nil {
			t.Errorf("非法凭据未被拒绝：%q", body)
		}
		if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "X-Bad") {
			t.Errorf("错误泄露路径或内容：%v", err)
		}
	}
	if _, err := LoadCookie("", filepath.Join(t.TempDir(), "missing-private-canary")); !errors.Is(err, ErrNoCredential) && strings.Contains(err.Error(), "canary") {
		t.Errorf("缺失文件错误泄露路径：%v", err)
	}
}

func TestLoadCookieEmpty(t *testing.T) {
	if _, err := LoadCookie("  ", ""); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("空凭据应返回 ErrNoCredential，实际 %v", err)
	}
}
