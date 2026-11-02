package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const audioProbeTimeout = 30 * time.Second

// FFmpegAudioExtractorOptions 的路径来自 [watch] 配置；Command 仅测试替换。
type FFmpegAudioExtractorOptions struct {
	FFmpegPath  string
	FFprobePath string
	TempDir     string
	Command     Commander
}

// FFmpegAudioExtractor 先探测音轨，再抽出单声道 16kHz WAV 给 ASR。
type FFmpegAudioExtractor struct {
	ffmpegPath  string
	ffprobePath string
	tempDir     string
	cmd         Commander
}

// NewFFmpegAudioExtractor 校验工具路径。
func NewFFmpegAudioExtractor(opts FFmpegAudioExtractorOptions) (*FFmpegAudioExtractor, error) {
	if strings.TrimSpace(opts.FFmpegPath) == "" || strings.TrimSpace(opts.FFprobePath) == "" ||
		strings.ContainsAny(opts.FFmpegPath+opts.FFprobePath, "\r\n\x00") ||
		strings.ContainsAny(opts.TempDir, "\r\n\x00") {
		return nil, errors.New("音频提取器参数无效：ffmpeg/ffprobe路径不合规")
	}
	cmd := opts.Command
	if cmd == nil {
		cmd = ExecCommander()
	}
	return &FFmpegAudioExtractor{ffmpegPath: opts.FFmpegPath, ffprobePath: opts.FFprobePath, tempDir: opts.TempDir, cmd: cmd}, nil
}

// Extract 返回临时 WAV 路径；无音轨返回 ErrNoAudio（告警，不是失败）。
func (e *FFmpegAudioExtractor) Extract(ctx context.Context, videoPath string) (string, error) {
	if strings.TrimSpace(videoPath) == "" {
		return "", errors.New("提取音频缺少视频路径")
	}
	probeCtx, cancel := context.WithTimeout(ctx, audioProbeTimeout)
	out, err := e.cmd.Run(probeCtx, e.ffprobePath,
		"-v", "error", "-select_streams", "a",
		"-show_entries", "stream=index", "-of", "csv=p=0", videoPath)
	cancel()
	if err != nil {
		return "", fmt.Errorf("探测音轨失败：%w", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return "", ErrNoAudio
	}

	file, err := os.CreateTemp(e.tempDir, "fish-watch-*.wav")
	if err != nil {
		return "", fmt.Errorf("创建音频临时文件失败：%w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("创建音频临时文件失败：%w", err)
	}
	extractCtx, extractCancel := context.WithTimeout(ctx, audioProbeTimeout)
	defer extractCancel()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", videoPath,
		"-vn", "-ac", "1", "-ar", "16000",
		"-c:a", "pcm_s16le",
		path,
	}
	if _, err := e.cmd.Run(extractCtx, e.ffmpegPath, args...); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("提取音频失败：%w", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		os.Remove(path)
		return "", errors.New("提取音频失败：输出文件为空")
	}
	return path, nil
}
