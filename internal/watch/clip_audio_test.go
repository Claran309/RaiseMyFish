package watch

// 本文件测「录 30 秒 → 抽音频 → ASR」串接：成功清理临时文件、无音轨告警、转写失败降级。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeClipRecorder struct {
	path    string
	err     error
	seconds []int
}

func (f *fakeClipRecorder) Record(_ context.Context, seconds int) (string, error) {
	f.seconds = append(f.seconds, seconds)
	return f.path, f.err
}

type fakeClipExtractor struct {
	path string
	err  error
}

func (f *fakeClipExtractor) Extract(context.Context, string) (string, error) {
	return f.path, f.err
}

type fakeClipTranscriber struct {
	text string
	err  error
}

func (f *fakeClipTranscriber) Transcribe(context.Context, string) (string, error) {
	return f.text, f.err
}

func TestClipAudioReturnsTranscriptAndCleansFiles(t *testing.T) {
	video := tempPath(t, "clip.ts")
	audio := tempPath(t, "clip.wav")
	writeTestFile(t, video, []byte("video"))
	writeTestFile(t, audio, []byte("audio"))
	rec := &fakeClipRecorder{path: video}
	clip, err := NewClipAudio(rec, &fakeClipExtractor{path: audio}, &fakeClipTranscriber{text: " 大家好 "}, 0)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	text, err := clip.Listen(context.Background())
	if err != nil {
		t.Fatalf("Listen 失败：%v", err)
	}
	if text != "大家好" {
		t.Errorf("转写未去空白：%q", text)
	}
	if len(rec.seconds) != 1 || rec.seconds[0] != DefaultClipAudioSeconds {
		t.Errorf("录制秒数不符：%v", rec.seconds)
	}
	if fileExists(video) || fileExists(audio) {
		t.Error("临时视频/音频未清理")
	}
}

func TestClipAudioNoAudioKeepsErrNoAudio(t *testing.T) {
	video := tempPath(t, "clip.ts")
	writeTestFile(t, video, []byte("video"))
	clip, err := NewClipAudio(&fakeClipRecorder{path: video}, &fakeClipExtractor{err: ErrNoAudio}, &fakeClipTranscriber{}, 0)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	_, err = clip.Listen(context.Background())
	if !errors.Is(err, ErrNoAudio) {
		t.Fatalf("无音轨错误被包装丢失：%v", err)
	}
	if fileExists(video) {
		t.Error("无音轨时临时视频未清理")
	}
}

func TestClipAudioTranscribeFailureCleansAndWraps(t *testing.T) {
	video := tempPath(t, "clip.ts")
	audio := tempPath(t, "clip.wav")
	writeTestFile(t, video, []byte("video"))
	writeTestFile(t, audio, []byte("audio"))
	clip, err := NewClipAudio(&fakeClipRecorder{path: video}, &fakeClipExtractor{path: audio}, &fakeClipTranscriber{err: errors.New("ASR 挂了")}, 0)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	_, err = clip.Listen(context.Background())
	if err == nil || !strings.Contains(err.Error(), "转写直播声音失败") {
		t.Fatalf("失败阶段未包装：%v", err)
	}
	if fileExists(video) || fileExists(audio) {
		t.Error("失败后临时视频/音频未清理")
	}
}

func TestClipAudioEmptyTranscriptIsError(t *testing.T) {
	video := tempPath(t, "clip.ts")
	audio := tempPath(t, "clip.wav")
	writeTestFile(t, video, []byte("video"))
	writeTestFile(t, audio, []byte("audio"))
	clip, err := NewClipAudio(&fakeClipRecorder{path: video}, &fakeClipExtractor{path: audio}, &fakeClipTranscriber{text: "   "}, 0)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := clip.Listen(context.Background()); err == nil {
		t.Fatal("空转写应显式报错")
	}
}

func TestNewClipAudioValidates(t *testing.T) {
	if _, err := NewClipAudio(nil, nil, nil, 30); err == nil {
		t.Fatal("缺少依赖应报错")
	}
	if _, err := NewClipAudio(&fakeClipRecorder{}, &fakeClipExtractor{}, &fakeClipTranscriber{}, 121); err == nil {
		t.Fatal("超长秒数应报错")
	}
}
