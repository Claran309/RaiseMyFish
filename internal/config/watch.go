package config

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Watch 是「看直播」流水线的部署配置（`[watch]`），默认关闭。
// 凭据只从 cookie_env 指向的环境变量读取，不写入 TOML/JSON。
type Watch struct {
	Enabled         bool   `toml:"enabled"`
	CurrentRoom     string `toml:"current_room"`
	StreamerName    string `toml:"streamer_name"`
	Source          string `toml:"source"`
	IntervalSeconds int    `toml:"interval_seconds"`
	Seconds         int    `toml:"seconds"`
	Frames          int    `toml:"frames"`
	ASRURL          string `toml:"asr_url"`
	StreamQuality   string `toml:"stream_quality"`
	CookieEnv       string `toml:"cookie_env"`
	FFmpegPath      string `toml:"ffmpeg_path"`
	FFprobePath     string `toml:"ffprobe_path"`
	TempDir         string `toml:"temp_dir"`
	LLMRoute        string `toml:"llm_route"`
	Cookie          string `toml:"-" json:"-"`
}

var watchEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var watchRoomPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func defaultWatch() Watch {
	return Watch{
		Source:          "stream",
		IntervalSeconds: 600,
		Seconds:         10,
		Frames:          4,
		ASRURL:          "http://127.0.0.1:8077/v1/audio/transcriptions",
		StreamQuality:   "origin",
		CookieEnv:       "FISH_BILI_COOKIE",
		FFmpegPath:      "ffmpeg",
		FFprobePath:     "ffprobe",
		LLMRoute:        "default",
	}
}

func (w *Watch) resolveEnvironment(lookup func(string) (string, bool), enabled bool) {
	if !enabled || !watchEnvName.MatchString(w.CookieEnv) {
		return
	}
	w.Cookie, _ = lookup(w.CookieEnv)
}

// validateIdentity 校验状态注入字段；即使看直播关闭也要校验，因为它们会进入 Brain prompt。
func (w Watch) validateIdentity() error {
	if w.CurrentRoom != "" && !watchRoomPattern.MatchString(w.CurrentRoom) {
		return errors.New("看直播 current_room 必须是1至20位数字房间号")
	}
	name := strings.TrimSpace(w.StreamerName)
	if utf8.RuneCountInString(name) > 32 || strings.ContainsAny(name, "\r\n\x00<>") {
		return errors.New("看直播 streamer_name 最多32个字符且不能包含控制字符或尖括号")
	}
	return nil
}

// Validate 只做静态校验；不访问网络、不执行 ffmpeg、不读取凭据文件。
func (w Watch) Validate() error {
	if err := w.validateIdentity(); err != nil {
		return err
	}
	if !w.Enabled {
		return nil
	}
	if w.Source == "screen" {
		return errors.New("看直播 screen 模式尚未实现，请使用 stream")
	}
	if w.Source != "stream" {
		return errors.New("看直播 source 只能是 stream 或 screen")
	}
	if w.IntervalSeconds < 10 || w.IntervalSeconds > 86400 {
		return errors.New("看直播间隔必须在10至86400秒之间")
	}
	if w.Seconds < 2 || w.Seconds > 120 {
		return errors.New("看直播秒数必须在2至120之间")
	}
	if w.Frames < 3 || w.Frames > 5 {
		return errors.New("看直播帧数必须在3至5之间")
	}
	if valid, _ := validEndpoint(w.ASRURL); !valid {
		return errors.New("看直播 asr_url 必须是HTTPS或本机HTTP地址")
	}
	switch w.StreamQuality {
	case "origin", "bluray", "ultra", "high", "smooth":
	default:
		return errors.New("看直播清晰度只能是 origin/bluray/ultra/high/smooth")
	}
	if !watchEnvName.MatchString(w.CookieEnv) {
		return errors.New("看直播 cookie_env 必须是合法环境变量名")
	}
	if strings.TrimSpace(w.Cookie) == "" || strings.ContainsAny(w.Cookie, "\r\n\x00") {
		return errors.New("看直播启用需要环境变量中的B站凭据")
	}
	if strings.TrimSpace(w.FFmpegPath) == "" || strings.TrimSpace(w.FFprobePath) == "" ||
		strings.ContainsAny(w.FFmpegPath+w.FFprobePath, "\r\n\x00") ||
		strings.ContainsAny(w.TempDir, "\r\n\x00") {
		return errors.New("看直播 ffmpeg/ffprobe/临时目录配置无效")
	}
	if strings.TrimSpace(w.LLMRoute) == "" || strings.ContainsAny(w.LLMRoute, "\r\n") {
		return errors.New("看直播 llm_route 不能为空")
	}
	return nil
}
