package watch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const frameProbeTimeout = 30 * time.Second

// FFmpegFramerOptions 的路径来自 [watch] 配置；Command 仅测试替换。
type FFmpegFramerOptions struct {
	FFmpegPath  string
	FFprobePath string
	TempDir     string
	Command     Commander
}

// FFmpegFramer 用 ffprobe 取时长，再按均匀间隔逐帧抽 JPG。
type FFmpegFramer struct {
	ffmpegPath  string
	ffprobePath string
	tempDir     string
	cmd         Commander
}

// NewFFmpegFramer 校验工具路径。
func NewFFmpegFramer(opts FFmpegFramerOptions) (*FFmpegFramer, error) {
	if strings.TrimSpace(opts.FFmpegPath) == "" || strings.TrimSpace(opts.FFprobePath) == "" ||
		strings.ContainsAny(opts.FFmpegPath+opts.FFprobePath, "\r\n\x00") ||
		strings.ContainsAny(opts.TempDir, "\r\n\x00") {
		return nil, errors.New("抽帧器参数无效：ffmpeg/ffprobe路径不合规")
	}
	cmd := opts.Command
	if cmd == nil {
		cmd = ExecCommander()
	}
	return &FFmpegFramer{ffmpegPath: opts.FFmpegPath, ffprobePath: opts.FFprobePath, tempDir: opts.TempDir, cmd: cmd}, nil
}

// Frames 返回 count 个真实生成的帧路径；输出目录由 Pipeline 清理。
//
// 失败清理归创建者（O06）：一旦返回 error，调用方拿不到目录路径，
// 就永远没机会删掉已生成的帧。所以在所有权移交前失败时，这里自己 RemoveAll。
func (f *FFmpegFramer) Frames(ctx context.Context, videoPath string, count int) (paths []string, err error) {
	if count < 1 || count > MaxFramesPerClip {
		return nil, fmt.Errorf("抽帧数必须在1至%d之间", MaxFramesPerClip)
	}
	if strings.TrimSpace(videoPath) == "" {
		return nil, errors.New("抽帧缺少视频路径")
	}
	duration, err := f.duration(ctx, videoPath)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(f.tempDir, "fish-watch-frames-*")
	if err != nil {
		return nil, fmt.Errorf("创建抽帧临时目录失败：%w", err)
	}
	// 所有权还没移交：任何一条返回路径上的失败都要自己回收目录，
	// 否则「第二帧失败」会在临时目录里留下一帧 JPG + 一个空目录。
	handedOver := false
	defer func() {
		if !handedOver {
			_ = os.RemoveAll(dir)
		}
	}()
	paths = make([]string, 0, count)
	for i := 0; i < count; i++ {
		timestamp := duration * (float64(i) + 0.5) / float64(count)
		out := filepath.Join(dir, fmt.Sprintf("frame-%02d.jpg", i))
		frameCtx, cancel := context.WithTimeout(ctx, frameProbeTimeout)
		args := []string{
			"-hide_banner", "-loglevel", "error", "-y",
			"-ss", strconv.FormatFloat(timestamp, 'f', 3, 64),
			"-i", videoPath,
			"-frames:v", "1",
			"-vf", "scale=960:-2",
			"-q:v", "2",
			out,
		}
		_, runErr := f.cmd.Run(frameCtx, f.ffmpegPath, args...)
		cancel()
		if runErr != nil {
			return nil, fmt.Errorf("抽帧失败（第%d帧）：%w", i+1, runErr)
		}
		info, statErr := os.Stat(out)
		if statErr != nil || info.Size() == 0 {
			return nil, fmt.Errorf("抽帧失败：第%d帧未生成", i+1)
		}
		paths = append(paths, out)
	}
	// 全部成功才把目录所有权交给调用方（由 Pipeline.cleanup 负责删除）。
	handedOver = true
	return paths, nil
}

func (f *FFmpegFramer) duration(ctx context.Context, videoPath string) (float64, error) {
	probeCtx, cancel := context.WithTimeout(ctx, frameProbeTimeout)
	defer cancel()
	out, err := f.cmd.Run(probeCtx, f.ffprobePath,
		"-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", videoPath)
	if err != nil {
		return 0, fmt.Errorf("探测视频时长失败：%w", err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || duration <= 0 || math.IsInf(duration, 0) || math.IsNaN(duration) {
		return 0, errors.New("探测视频时长失败：时长无效")
	}
	return duration, nil
}
