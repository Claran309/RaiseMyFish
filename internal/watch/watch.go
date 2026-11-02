// Package watch 实现「看直播」的文件式流水线：录制 → 抽帧 → 读画面 / 听声音。
//
// 各环节通过接口注入，任何一环失败都只降级为部分结果，不把错误抛回弹幕主链路。
package watch

import (
	"context"
	"errors"
)

// DefaultPrompt 是读帧的固定提示词。
const DefaultPrompt = "描述画面：主播在做什么/画面文字/氛围"

// MaxFramesPerClip 与配置校验口径一致：一段录像最多看 5 帧。
const MaxFramesPerClip = 5

// Clip 是一段录制结果；路径都由本次运行的临时目录产生，由 Pipeline 负责清理。
type Clip struct {
	VideoPath string
	Frames    []string
	AudioPath string
	Seconds   int
}

// Recorder 录制直播流（或屏幕）指定秒数，返回视频路径。
type Recorder interface {
	Record(ctx context.Context, seconds int) (string, error)
}

// Framer 从视频中均匀抽帧；每一帧必须真实生成，不得用占位文件充数。
type Framer interface {
	Frames(ctx context.Context, videoPath string, count int) ([]string, error)
}

// Describer 把帧交给视觉模型，返回画面描述。
type Describer interface {
	Describe(ctx context.Context, frames []string, prompt string) (string, error)
}

// Transcriber 把音频转成文字。
type Transcriber interface {
	Transcribe(ctx context.Context, audioPath string) (string, error)
}

// AudioExtractor 从视频抽出单声道 16kHz WAV；没有音轨时返回 ErrNoAudio。
type AudioExtractor interface {
	Extract(ctx context.Context, videoPath string) (string, error)
}

// ErrNoAudio 表示视频本身没有音轨；它是告警，不是录制失败。
var ErrNoAudio = errors.New("视频没有音轨")

// Failure 记录一个降级环节；Err 已脱敏，可直接写日志。
type Failure struct {
	Stage string
	Err   error
}

func (f Failure) Error() string { return f.Stage + ": " + f.Err.Error() }

// Result 是 Pipeline 的部分结果：失败只进 Failures，不阻断其它环节。
type Result struct {
	Description string
	Transcript  string
	Warnings    []string
	Failures    []Failure
}

// Degraded 表示至少有一个环节失败。
func (r Result) Degraded() bool { return len(r.Failures) > 0 }
