package config

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Gift 是送礼通道配置（`[gift]`）：默认关闭且默认 dry-run。
// 凭据只从环境变量读取，不写入 TOML/JSON；真实送礼需显式 real=true。
type Gift struct {
	Enabled    bool   `toml:"enabled"`
	Real       bool   `toml:"real"`
	GiftID     string `toml:"gift_id"`
	Num        int    `toml:"num"`
	DailyLimit int    `toml:"daily_limit"`
	NodePath   string `toml:"node_path"`
	ScriptPath string `toml:"script_path"`
	TimeoutMS  int    `toml:"timeout_ms"`
	Cookie     string `toml:"-" json:"-"`
	CookieFile string `toml:"-" json:"-"`
}

var giftIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)

func defaultGift() Gift {
	return Gift{
		Enabled: false,
		Real:    false,
		// 31036 = 小花花（直购，100 price = 1 电池），2026-11-02 公共礼物面板核对。
		GiftID:     "31036",
		Num:        1,
		DailyLimit: 1,
		NodePath:   "node",
		ScriptPath: "tools/bili-whale/gift.mjs",
		TimeoutMS:  30000,
	}
}

func (g *Gift) resolveEnvironment(lookup func(string) (string, bool), enabled bool) {
	// dry-run 不读凭据，避免误配值进入进程内存或子进程环境。
	if !enabled || !g.Real {
		return
	}
	g.Cookie, _ = lookup("FISH_BILI_COOKIE")
	if g.Cookie == "" {
		g.CookieFile, _ = lookup("FISH_BILI_COOKIE_FILE")
	}
}

// Validate 只做静态校验，不读取凭据文件、执行Node或访问平台。
func (g Gift) Validate() error {
	if !g.Enabled {
		return nil
	}
	if !giftIDPattern.MatchString(g.GiftID) {
		return errors.New("礼物ID必须是平台十进制ID")
	}
	id, err := strconv.ParseUint(g.GiftID, 10, 64)
	if err != nil || id > 9007199254740991 {
		return errors.New("礼物ID超出安全整数范围")
	}
	if g.Num < 1 || g.Num > 100 {
		return errors.New("单次礼物数量必须在1至100之间")
	}
	if g.DailyLimit < 1 || g.DailyLimit > 100 {
		return errors.New("送礼每日限额必须在1至100之间")
	}
	if g.TimeoutMS < 1000 || g.TimeoutMS > 600000 {
		return errors.New("送礼超时必须在1至600秒内")
	}
	if strings.TrimSpace(g.NodePath) == "" || strings.TrimSpace(g.ScriptPath) == "" ||
		strings.ContainsAny(g.NodePath+g.ScriptPath, "\r\n\x00") {
		return errors.New("送礼node/脚本路径配置无效")
	}
	if g.Real {
		if g.Num > g.DailyLimit {
			return errors.New("送礼数量不能超过每日限额")
		}
		if (g.Cookie == "" && g.CookieFile == "") || strings.ContainsAny(g.Cookie+g.CookieFile, "\r\n\x00") {
			return errors.New("真实送礼需要环境变量中的B站凭据")
		}
	}
	return nil
}
