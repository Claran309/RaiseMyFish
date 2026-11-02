package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPlayInfoURL = "https://api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo"
	watchUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) RaiseMyFish-watch/0.12"
	maxPlayInfoBytes   = 2 << 20
	// 录制在目标秒数之外多给的收尾时间；超时由 ctx 终止 ffmpeg。
	recordGraceSeconds = 30
	// 主动弹幕只截一帧；直播流首包可能较慢，仍由 ctx 强制收敛。
	snapshotTimeout = 15 * time.Second
)

var streamRoomID = regexp.MustCompile(`^[0-9]{1,128}$`)

// streamQualities 同时用于配置校验与请求 qn；名字比原始数字更适合写进 TOML。
var streamQualities = map[string]int{
	"origin": 10000,
	"bluray": 400,
	"ultra":  250,
	"high":   150,
	"smooth": 80,
}

// ParseQuality 把配置里的清晰度名字转成 B站 qn。
func ParseQuality(name string) (int, error) {
	qn, ok := streamQualities[name]
	if !ok {
		return 0, fmt.Errorf("未知的直播清晰度 %q", name)
	}
	return qn, nil
}

// StreamRecorderOptions 中凭据只来自环境，不写入 TOML/日志。
type StreamRecorderOptions struct {
	RoomID     string
	Cookie     string
	Quality    string
	FFmpegPath string
	TempDir    string
	HTTPClient *http.Client
	Timeout    time.Duration
	Command    Commander
}

// StreamRecorder 先取 room_play_info，再让 ffmpeg 按秒数录制直播流。
type StreamRecorder struct {
	roomID     string
	cookie     string
	quality    int
	ffmpegPath string
	tempDir    string
	client     *http.Client
	cmd        Commander
	playInfo   string
}

// NewStreamRecorder 校验静态参数并克隆 HTTP client（禁重定向、限超时）。
func NewStreamRecorder(opts StreamRecorderOptions) (*StreamRecorder, error) {
	quality, err := ParseQuality(opts.Quality)
	if err != nil {
		return nil, err
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if !streamRoomID.MatchString(opts.RoomID) || strings.Trim(opts.RoomID, "0") == "" ||
		strings.TrimSpace(opts.Cookie) == "" || strings.ContainsAny(opts.Cookie, "\r\n\x00") ||
		strings.TrimSpace(opts.FFmpegPath) == "" || strings.ContainsAny(opts.FFmpegPath, "\r\n\x00") ||
		strings.ContainsAny(opts.TempDir, "\r\n\x00") || timeout < time.Second || timeout > 60*time.Second {
		return nil, errors.New("录制器参数无效：房间号/凭据/ffmpeg路径或超时不合规")
	}
	base := http.DefaultClient
	if opts.HTTPClient != nil {
		base = opts.HTTPClient
	}
	client := *base
	client.Timeout = timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cmd := opts.Command
	if cmd == nil {
		cmd = ExecCommander()
	}
	return &StreamRecorder{
		roomID:     opts.RoomID,
		cookie:     opts.Cookie,
		quality:    quality,
		ffmpegPath: opts.FFmpegPath,
		tempDir:    opts.TempDir,
		client:     &client,
		cmd:        cmd,
		playInfo:   defaultPlayInfoURL,
	}, nil
}

// Record 返回录制文件路径；任何一步拿不到真实画面都显式报错。
func (r *StreamRecorder) Record(ctx context.Context, seconds int) (string, error) {
	if seconds < 1 || seconds > 600 {
		return "", errors.New("录制秒数必须在1至600之间")
	}
	streamURL, err := r.streamURL(ctx)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(r.tempDir, "fish-watch-*.ts")
	if err != nil {
		return "", fmt.Errorf("创建录制临时文件失败：%w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("创建录制临时文件失败：%w", err)
	}

	headers := "Cookie: " + r.cookie + "\r\n" +
		"Referer: https://live.bilibili.com/" + r.roomID + "\r\n" +
		"User-Agent: " + watchUserAgent
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-headers", headers,
		"-i", streamURL,
		"-t", strconv.Itoa(seconds),
		"-c", "copy",
		"-f", "mpegts",
		path,
	}
	recordCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds+recordGraceSeconds)*time.Second)
	defer cancel()
	if _, err := r.cmd.Run(recordCtx, r.ffmpegPath, args...); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("录制直播流失败：%w", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		os.Remove(path)
		return "", errors.New("录制直播流失败：输出文件为空")
	}
	return path, nil
}

// Snapshot 从当前直播流抽 1 张 JPG，返回临时文件路径；调用方负责删除。
// 与 Record 复用同一套地址解析和 ffmpeg 参数，任何一步失败都不返回占位文件。
func (r *StreamRecorder) Snapshot(ctx context.Context) (string, error) {
	streamURL, err := r.streamURL(ctx)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(r.tempDir, "fish-proactive-*.jpg")
	if err != nil {
		return "", fmt.Errorf("创建截帧临时文件失败：%w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("创建截帧临时文件失败：%w", err)
	}
	headers := "Cookie: " + r.cookie + "\r\n" +
		"Referer: https://live.bilibili.com/" + r.roomID + "\r\n" +
		"User-Agent: " + watchUserAgent
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-headers", headers,
		"-i", streamURL,
		"-frames:v", "1",
		"-vf", "scale=960:-2",
		"-q:v", "2",
		path,
	}
	snapCtx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	if _, err := r.cmd.Run(snapCtx, r.ffmpegPath, args...); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("截取直播画面失败：%w", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		os.Remove(path)
		return "", errors.New("截取直播画面失败：输出文件为空")
	}
	return path, nil
}

type playInfoResponse struct {
	Code int `json:"code"`
	Data struct {
		PlayurlInfo *struct {
			Playurl struct {
				Stream []struct {
					Format []struct {
						Codec []struct {
							BaseURL string `json:"base_url"`
							URLInfo []struct {
								Host  string `json:"host"`
								Extra string `json:"extra"`
							} `json:"url_info"`
						} `json:"codec"`
					} `json:"format"`
				} `json:"stream"`
			} `json:"playurl"`
		} `json:"playurl_info"`
	} `json:"data"`
}

func (r *StreamRecorder) streamURL(ctx context.Context) (string, error) {
	endpoint, err := url.Parse(r.playInfo)
	if err != nil {
		return "", errors.New("直播流地址接口配置无效")
	}
	query := endpoint.Query()
	query.Set("room_id", r.roomID)
	query.Set("qn", strconv.Itoa(r.quality))
	query.Set("platform", "web")
	query.Set("protocol", "0,1")
	query.Set("format", "0,1,2")
	query.Set("codec", "0,1")
	query.Set("ptype", "8")
	query.Set("dolby", "5")
	query.Set("panorama", "1")
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", errors.New("构造直播流地址请求失败")
	}
	req.Header.Set("Cookie", r.cookie)
	req.Header.Set("Referer", "https://live.bilibili.com/"+r.roomID)
	req.Header.Set("User-Agent", watchUserAgent)
	res, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求直播流地址失败：%w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("请求直播流地址失败：HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxPlayInfoBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取直播流地址响应失败：%w", err)
	}
	if len(body) > maxPlayInfoBytes {
		return "", errors.New("直播流地址响应过大")
	}
	var decoded playInfoResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", errors.New("直播流地址响应格式无法识别")
	}
	if decoded.Code != 0 {
		return "", fmt.Errorf("获取直播流地址失败：接口代码 %d", decoded.Code)
	}
	info := decoded.Data.PlayurlInfo
	if info == nil {
		return "", errors.New("获取直播流地址失败：直播未开播或没有可用流")
	}
	for _, stream := range info.Playurl.Stream {
		for _, format := range stream.Format {
			for _, codec := range format.Codec {
				if codec.BaseURL == "" || len(codec.URLInfo) == 0 {
					continue
				}
				candidate := codec.URLInfo[0].Host + codec.BaseURL + codec.URLInfo[0].Extra
				parsed, err := url.Parse(candidate)
				if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
					continue
				}
				return parsed.String(), nil
			}
		}
	}
	return "", errors.New("获取直播流地址失败：直播未开播或没有可用流")
}
