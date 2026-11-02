package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stubRecorder struct {
	path   string
	err    error
	called bool
}

func (r *stubRecorder) Record(_ context.Context, _ int) (string, error) {
	r.called = true
	return r.path, r.err
}

type stubFramer struct {
	frames []string
	err    error
	called bool
}

func (f *stubFramer) Frames(_ context.Context, _ string, _ int) ([]string, error) {
	f.called = true
	return f.frames, f.err
}

type stubDescriber struct {
	text   string
	err    error
	called bool
	frames []string
	prompt string
}

func (d *stubDescriber) Describe(_ context.Context, frames []string, prompt string) (string, error) {
	d.called = true
	d.frames = frames
	d.prompt = prompt
	return d.text, d.err
}

type stubTranscriber struct {
	text   string
	err    error
	called bool
}

func (t *stubTranscriber) Transcribe(_ context.Context, _ string) (string, error) {
	t.called = true
	return t.text, t.err
}

type stubAudio struct {
	path   string
	err    error
	called bool
}

func (a *stubAudio) Extract(_ context.Context, _ string) (string, error) {
	a.called = true
	return a.path, a.err
}

type pipelineFixture struct {
	video       string
	framesDir   string
	frames      []string
	audio       string
	recorder    *stubRecorder
	framer      *stubFramer
	describer   *stubDescriber
	transcriber *stubTranscriber
	audioStub   *stubAudio
	pipeline    *Pipeline
}

func newPipelineFixture(t *testing.T) *pipelineFixture {
	t.Helper()
	root := t.TempDir()
	video := filepath.Join(root, "clip.ts")
	writeTestFile(t, video, []byte("video"))
	framesDir := filepath.Join(root, "frames")
	if err := os.Mkdir(framesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	frames := []string{filepath.Join(framesDir, "frame-00.png")}
	writeSolidPNG(t, frames[0], 128)
	audio := filepath.Join(root, "clip.wav")
	writeTestFile(t, audio, []byte("wav"))

	fixture := &pipelineFixture{
		video:       video,
		framesDir:   framesDir,
		frames:      frames,
		audio:       audio,
		recorder:    &stubRecorder{path: video},
		framer:      &stubFramer{frames: frames},
		describer:   &stubDescriber{text: "主播在喝水"},
		transcriber: &stubTranscriber{text: "大家好"},
		audioStub:   &stubAudio{path: audio},
	}
	pipeline, err := NewPipeline(fixture.recorder, fixture.framer, fixture.describer, fixture.transcriber, fixture.audioStub, PipelineOptions{
		Seconds: 2,
		Frames:  4,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.pipeline = pipeline
	return fixture
}

func TestPipelineSuccessAndCleanup(t *testing.T) {
	fixture := newPipelineFixture(t)
	result, err := fixture.pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Description != "主播在喝水" || result.Transcript != "大家好" || result.Degraded() || len(result.Warnings) != 0 {
		t.Fatalf("结果不符：%+v", result)
	}
	if fixture.describer.prompt != DefaultPrompt || len(fixture.describer.frames) != 1 {
		t.Fatal("读帧未使用固定提示词或帧")
	}
	for _, path := range []string{fixture.video, fixture.frames[0], fixture.audio} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("临时文件未清理：%s", path)
		}
	}
	if _, err := os.Stat(fixture.framesDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("空帧目录未清理")
	}
}

func TestPipelineRecordFailureStopsEarly(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.recorder.err = errors.New("直播未开播")
	result, err := fixture.pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 || result.Failures[0].Stage != StageRecord {
		t.Fatalf("降级记录不符：%+v", result.Failures)
	}
	if fixture.framer.called || fixture.describer.called || fixture.audioStub.called {
		t.Fatal("录制失败后仍继续后续环节")
	}
}

func TestPipelineFramesFailureStillTranscribes(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.framer.err = errors.New("ffmpeg 抽帧失败")
	result, err := fixture.pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 || result.Failures[0].Stage != StageFrames {
		t.Fatalf("降级记录不符：%+v", result.Failures)
	}
	if fixture.describer.called {
		t.Fatal("没有帧仍调用读图")
	}
	if result.Transcript != "大家好" || !fixture.transcriber.called {
		t.Fatal("抽帧失败不应阻断转写")
	}
}

func TestPipelineDescribeFailureStillTranscribes(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.describer.err = errors.New("视觉模型调用失败")
	result, err := fixture.pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 || result.Failures[0].Stage != StageDescribe {
		t.Fatalf("降级记录不符：%+v", result.Failures)
	}
	if result.Transcript != "大家好" {
		t.Fatal("读图失败不应阻断转写")
	}
}

func TestPipelineNoAudioIsWarningNotFailure(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.audioStub.err = ErrNoAudio
	result, err := fixture.pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Degraded() || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "无音轨") {
		t.Fatalf("无音轨应只告警：%+v", result)
	}
	if fixture.transcriber.called {
		t.Fatal("无音轨仍调用转写")
	}
	if result.Description != "主播在喝水" {
		t.Fatal("无音轨不应影响画面描述")
	}
}

func TestPipelineTranscribeFailureKeepsDescription(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.transcriber.err = errors.New("ASR服务不可用")
	result, err := fixture.pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 || result.Failures[0].Stage != StageTranscribe {
		t.Fatalf("降级记录不符：%+v", result.Failures)
	}
	if result.Description != "主播在喝水" || result.Transcript != "" {
		t.Fatalf("部分结果不符：%+v", result)
	}
}

func TestPipelineCancellationReturnsError(t *testing.T) {
	fixture := newPipelineFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fixture.recorder.err = context.Canceled
	result, err := fixture.pipeline.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未返回错误：%v", err)
	}
	if len(result.Failures) != 1 || result.Failures[0].Stage != StageRecord {
		t.Fatalf("取消未记录降级：%+v", result.Failures)
	}
}

func TestNewPipelineRejectsMissingDependencies(t *testing.T) {
	fixture := newPipelineFixture(t)
	if _, err := NewPipeline(nil, fixture.framer, fixture.describer, fixture.transcriber, fixture.audioStub, PipelineOptions{Seconds: 2, Frames: 4}); err == nil {
		t.Fatal("接受空录制器")
	}
	if _, err := NewPipeline(fixture.recorder, fixture.framer, fixture.describer, fixture.transcriber, fixture.audioStub, PipelineOptions{Seconds: 2, Frames: 6}); err == nil {
		t.Fatal("接受超量帧数")
	}
	if _, err := NewPipeline(fixture.recorder, fixture.framer, fixture.describer, fixture.transcriber, fixture.audioStub, PipelineOptions{Seconds: 0, Frames: 4}); err == nil {
		t.Fatal("接受非法秒数")
	}
}
