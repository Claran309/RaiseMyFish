package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func newTestExtractor(t *testing.T, commander *fakeCommander) *FFmpegAudioExtractor {
	t.Helper()
	extractor, err := NewFFmpegAudioExtractor(FFmpegAudioExtractorOptions{
		FFmpegPath:  "ffmpeg",
		FFprobePath: "ffprobe",
		TempDir:     t.TempDir(),
		Command:     commander,
	})
	if err != nil {
		t.Fatal(err)
	}
	return extractor
}

func TestFFmpegAudioExtractorReportsNoAudio(t *testing.T) {
	commander := &fakeCommander{run: func(name string, _ ...string) ([]byte, error) {
		if name == "ffprobe" {
			return []byte("\n"), nil
		}
		return nil, errors.New("不应调用ffmpeg")
	}}
	extractor := newTestExtractor(t, commander)
	_, err := extractor.Extract(context.Background(), "video.ts")
	if !errors.Is(err, ErrNoAudio) {
		t.Fatalf("无音轨应返回ErrNoAudio：%v", err)
	}
	if commander.count("ffmpeg") != 0 {
		t.Fatal("无音轨仍调用ffmpeg")
	}
}

func TestFFmpegAudioExtractorWritesWAV(t *testing.T) {
	commander := &fakeCommander{run: func(name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			return []byte("0\n"), nil
		}
		return nil, createLastArg(t, args, []byte("wav-bytes"))
	}}
	extractor := newTestExtractor(t, commander)
	path, err := extractor.Extract(context.Background(), "video.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !fileExists(path) || !strings.HasSuffix(path, ".wav") {
		t.Fatal("未生成wav")
	}
	args := strings.Join(commander.calls[1].Args, " ")
	if !strings.Contains(args, "-ar 16000") || !strings.Contains(args, "-ac 1") || !strings.Contains(args, "-c:a pcm_s16le") {
		t.Fatalf("ffmpeg参数不符合ASR要求：%s", args)
	}
}

func TestFFmpegAudioExtractorProbeAndExtractFailures(t *testing.T) {
	commander := &fakeCommander{run: func(name string, _ ...string) ([]byte, error) {
		if name == "ffprobe" {
			return nil, errors.New("ffprobe 执行失败（[url]）：exit status 1")
		}
		return nil, nil
	}}
	extractor := newTestExtractor(t, commander)
	if _, err := extractor.Extract(context.Background(), "video.ts"); err == nil || !strings.Contains(err.Error(), "探测音轨失败") {
		t.Fatalf("探测失败未报错：%v", err)
	}

	commander.run = func(name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			return []byte("0\n"), nil
		}
		return nil, errors.New("ffmpeg 执行失败（[url]）：exit status 1")
	}
	if _, err := extractor.Extract(context.Background(), "video.ts"); err == nil || !strings.Contains(err.Error(), "提取音频失败") {
		t.Fatalf("提取失败未报错：%v", err)
	}

	if _, err := extractor.Extract(context.Background(), " "); err == nil {
		t.Fatal("接受空视频路径")
	}
}
