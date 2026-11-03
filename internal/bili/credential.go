package bili

// 本文件给纯 Go 的评论采集/发送复用同一套凭据解析：
// 显式 Cookie 优先，其次读文件（原始 Cookie 串或浏览器扩展导出的 JSON）。
// 错误只说明类别，不携带文件路径或文件内容。

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
)

// maxCookieBytes 与 Node 采集脚本的读取上限一致。
const maxCookieBytes = 64 * 1024

var sessdataPattern = regexp.MustCompile(`(?:^|;\s*)SESSDATA=[^;\s]+`)

func hasControl(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// LoadCookie 读取并校验 B站 Cookie 头；空值返回 ErrNoCredential。
func LoadCookie(cookie, file string) (string, error) {
	value := strings.TrimSpace(cookie)
	if value == "" {
		if strings.TrimSpace(file) == "" {
			return "", ErrNoCredential
		}
		raw, err := readCredentialFile(file)
		if err != nil {
			return "", err
		}
		value = raw
		if strings.HasPrefix(value, "{") {
			value, err = cookieFromJSON(value)
			if err != nil {
				return "", err
			}
		}
	}
	if value == "" || len(value) > maxCookieBytes || hasControl(value) || !sessdataPattern.MatchString(value) {
		return "", errors.New("B站凭据格式非法")
	}
	return value, nil
}

func readCredentialFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("读取B站凭据文件失败")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("B站凭据文件无效")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCookieBytes+1))
	if err != nil || len(data) > maxCookieBytes {
		return "", errors.New("B站凭据文件无效")
	}
	return strings.TrimSpace(string(data)), nil
}

// cookieFromJSON 与 Node 侧格式一致：sessdata 必填，bili_jct/buvid3 可选。
func cookieFromJSON(raw string) (string, error) {
	var data struct {
		Sessdata string `json:"sessdata"`
		BiliJct  string `json:"bili_jct"`
		Buvid3   string `json:"buvid3"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil || data.Sessdata == "" {
		return "", errors.New("B站凭据文件无效")
	}
	parts := make([]string, 0, 3)
	for _, item := range [][2]string{{"SESSDATA", data.Sessdata}, {"bili_jct", data.BiliJct}, {"buvid3", data.Buvid3}} {
		if item[1] == "" {
			continue
		}
		if strings.ContainsAny(item[1], ";") || hasControl(item[1]) {
			return "", errors.New("B站凭据文件无效")
		}
		parts = append(parts, item[0]+"="+item[1])
	}
	return strings.Join(parts, "; "), nil
}

// CSRFToken 返回 Cookie 里的 bili_jct；写接口缺少它时调用方应拒绝执行。
func CSRFToken(cookie string) string { return parseCSRF(cookie) }
