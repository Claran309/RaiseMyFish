package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// 流水线环节名，用于降级记录与日志。
const (
	StageRecord     = "record"
	StageFrames     = "frames"
	StageBrightness = "brightness"
	StageDescribe   = "describe"
	StageAudio      = "audio"
	StageTranscribe = "transcribe"
)

// PipelineOptions 控制每次看的时长、帧数与固定提示词。
type PipelineOptions struct {
	Seconds int
	Frames  int
	Prompt  string
	Logger  *zap.Logger
}

// Pipeline 串联录制/抽帧/读图/转写；任何一环失败只降级。
type Pipeline struct {
	recorder    Recorder
	framer      Framer
	describer   Describer
	transcriber Transcriber
	audio       AudioExtractor
	logger      *zap.Logger
	seconds     int
	frames      int
	prompt      string
}

// NewPipeline 拒绝缺环节的装配，避免运行到一半才发现没接。
func NewPipeline(recorder Recorder, framer Framer, describer Describer, transcriber Transcriber, audio AudioExtractor, opts PipelineOptions) (*Pipeline, error) {
	if recorder == nil || framer == nil || describer == nil || transcriber == nil || audio == nil {
		return nil, errors.New("看直播流水线缺少必要环节")
	}
	if opts.Seconds < 1 || opts.Seconds > 600 {
		return nil, errors.New("看直播秒数必须在1至600之间")
	}
	if opts.Frames < 1 || opts.Frames > MaxFramesPerClip {
		return nil, errors.New("看直播帧数必须在1至5之间")
	}
	prompt := strings.TrimSpace(opts.Prompt)
	if prompt == "" {
		prompt = DefaultPrompt
	}
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Pipeline{
		recorder:    recorder,
		framer:      framer,
		describer:   describer,
		transcriber: transcriber,
		audio:       audio,
		logger:      logger,
		seconds:     opts.Seconds,
		frames:      opts.Frames,
		prompt:      prompt,
	}, nil
}

// Run 返回部分结果：环节失败写入 Failures，不向调用方抛业务错误；
// 只有 ctx 被取消/超时才返回该错误，便于主链路及时退出。
// 调用方应把它放在独立 goroutine 中，避免录制时长阻塞弹幕闭环。
func (p *Pipeline) Run(ctx context.Context) (Result, error) {
	var result Result
	video, err := p.recorder.Record(ctx, p.seconds)
	if err != nil {
		p.fail(&result, StageRecord, err)
		return result, ctx.Err()
	}
	clip := Clip{VideoPath: video, Seconds: p.seconds}
	// 必须用闭包：defer p.cleanup(clip) 会在 defer 时复制，后续补的帧/音频不会被清理。
	defer func() { p.cleanup(clip) }()

	frames, err := p.framer.Frames(ctx, video, p.frames)
	if err != nil {
		p.fail(&result, StageFrames, err)
	} else {
		clip.Frames = frames
		if warning, warningErr := CheckBrightness(frames); warningErr != nil {
			p.fail(&result, StageBrightness, warningErr)
		} else if warning != "" {
			result.Warnings = append(result.Warnings, warning)
			p.logger.Warn("看直播告警", zap.String("detail", warning))
		}
		if description, describeErr := p.describer.Describe(ctx, frames, p.prompt); describeErr != nil {
			p.fail(&result, StageDescribe, describeErr)
		} else {
			result.Description = description
		}
	}

	audioPath, err := p.audio.Extract(ctx, video)
	switch {
	case errors.Is(err, ErrNoAudio):
		result.Warnings = append(result.Warnings, "无音轨，跳过转写")
		p.logger.Warn("看直播告警", zap.String("detail", "无音轨，跳过转写"))
	case err != nil:
		p.fail(&result, StageAudio, err)
	default:
		clip.AudioPath = audioPath
		transcript, transcribeErr := p.transcriber.Transcribe(ctx, audioPath)
		if transcribeErr != nil {
			p.fail(&result, StageTranscribe, transcribeErr)
		} else {
			result.Transcript = transcript
		}
	}
	return result, ctx.Err()
}

func (p *Pipeline) fail(result *Result, stage string, err error) {
	result.Failures = append(result.Failures, Failure{Stage: stage, Err: err})
	p.logger.Warn("看直播环节失败，已降级", zap.String("stage", stage), zap.Error(err))
}

// cleanup 只删本次产生的文件；帧目录仅在删空后移除，避免误删公共临时目录。
func (p *Pipeline) cleanup(clip Clip) {
	if clip.VideoPath != "" {
		_ = os.Remove(clip.VideoPath)
	}
	if len(clip.Frames) > 0 {
		for _, frame := range clip.Frames {
			_ = os.Remove(frame)
		}
		_ = os.Remove(filepath.Dir(clip.Frames[0]))
	}
	if clip.AudioPath != "" {
		_ = os.Remove(clip.AudioPath)
	}
}
