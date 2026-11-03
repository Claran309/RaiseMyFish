package config

import (
	"errors"
	"regexp"
	"strings"
)

// Collectors仅包含已实现的采集设置；私信/QQ仍不可启用。
type Collectors struct {
	Live    LiveCollector    `toml:"live"`
	Comment CommentCollector `toml:"comment"`
}

// LiveCollector凭据只从环境解析，禁止TOML/JSON回显。
type LiveCollector struct {
	RoomID           string `toml:"room_id"`
	SelfID           string `toml:"self_id"`
	NodePath         string `toml:"node_path"`
	ScriptPath       string `toml:"script_path"`
	HTTPTimeoutMS    int    `toml:"http_timeout_ms"`
	ConnectTimeoutMS int    `toml:"connect_timeout_ms"`
	HeartbeatMS      int    `toml:"heartbeat_ms"`
	IdleMS           int    `toml:"idle_ms"`
	StartupMS        int    `toml:"startup_ms"`
	BackoffMinMS     int    `toml:"backoff_min_ms"`
	BackoffMaxMS     int    `toml:"backoff_max_ms"`
	StableAfterMS    int    `toml:"stable_after_ms"`
	MaxFailures      int    `toml:"max_failures"`
	Cookie           string `toml:"-" json:"-"`
	CookieFile       string `toml:"-" json:"-"`
}

func defaultLiveCollector() LiveCollector {
	return LiveCollector{NodePath: "node", ScriptPath: "tools/bili-whale/danmaku-await.mjs", HTTPTimeoutMS: 10000, ConnectTimeoutMS: 10000, HeartbeatMS: 30000, IdleMS: 90000, StartupMS: 45000, BackoffMinMS: 1000, BackoffMaxMS: 60000, StableAfterMS: 30000, MaxFailures: 8}
}
func (l *LiveCollector) resolveEnvironment(lookup func(string) (string, bool), enabled bool) {
	if !enabled {
		return
	}
	for key, dst := range map[string]*string{"FISH_BILI_ROOM_ID": &l.RoomID, "FISH_BILI_SELF_ID": &l.SelfID, "FISH_BILI_COOKIE": &l.Cookie, "FISH_BILI_COOKIE_FILE": &l.CookieFile} {
		if value, ok := lookup(key); ok {
			*dst = value
		}
	}
}

var positiveLiveID = regexp.MustCompile(`^[0-9]{1,128}$`)

// Validate只检查静态配置，不读文件、不运行Node或访问账号。
func (l LiveCollector) Validate(enabled bool) error {
	if !enabled {
		return nil
	}
	if !positiveLiveID.MatchString(l.RoomID) || strings.Trim(l.RoomID, "0") == "" || !positiveLiveID.MatchString(l.SelfID) || strings.Trim(l.SelfID, "0") == "" || strings.TrimSpace(l.NodePath) == "" || strings.TrimSpace(l.ScriptPath) == "" || (l.Cookie == "" && l.CookieFile == "") || strings.ContainsAny(l.Cookie+l.CookieFile, "\r\n\x00") {
		return errors.New("直播采集房间/自身身份/运行时或凭据配置无效")
	}
	for _, v := range []int{l.HTTPTimeoutMS, l.ConnectTimeoutMS, l.HeartbeatMS, l.IdleMS, l.StartupMS, l.BackoffMinMS, l.BackoffMaxMS, l.StableAfterMS} {
		if v < 1000 || v > 600000 {
			return errors.New("直播采集时限必须在1至600秒内")
		}
	}
	if l.IdleMS <= l.HeartbeatMS || l.StartupMS < 3*l.HTTPTimeoutMS+l.ConnectTimeoutMS || l.BackoffMinMS > l.BackoffMaxMS || l.BackoffMaxMS > 60000 || l.MaxFailures < 1 || l.MaxFailures > 32 {
		return errors.New("直播采集存活/启动时限或重连参数无效")
	}
	return nil
}
