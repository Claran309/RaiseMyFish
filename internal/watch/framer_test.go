package watch

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func newTestFramer(t *testing.T, commander *fakeCommander) *FFmpegFramer {
	t.Helper()
	framer, err := NewFFmpegFramer(FFmpegFramerOptions{
		FFmpegPath:  "ffmpeg",
		FFprobePath: "ffprobe",
		TempDir:     t.TempDir(),
		Command:     commander,
	})
	if err != nil {
		t.Fatal(err)
	}
	return framer
}

func TestFFmpegFramerExtractsEvenlySpacedFrames(t *testing.T) {
	commander := &fakeCommander{run: func(name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			return []byte("10.000000\n"), nil
		}
		return nil, createLastArg(t, args, []byte("jpeg-bytes"))
	}}
	framer := newTestFramer(t, commander)
	frames, err := framer.Frames(context.Background(), "video.ts", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 4 {
		t.Fatalf("帧数=%d", len(frames))
	}
	// 均匀间隔：10s / 4 → 1.25, 3.75, 6.25, 8.75
	for i, frame := range frames {
		if !fileExists(frame) {
			t.Fatalf("第%d帧未生成", i+1)
		}
		want := strconv.FormatFloat(10*(float64(i)+0.5)/4, 'f', 3, 64)
		args := strings.Join(commander.calls[i+1].Args, " ")
		if !strings.Contains(args, "-ss "+want) {
			t.Fatalf("第%d帧时间戳不是 %s：%s", i+1, want, args)
		}
		if !strings.Contains(args, "scale=960:-2") {
			t.Fatal("抽帧未限制尺寸")
		}
	}
}

func TestFFmpegFramerRejectsBadDurationAndMissingFrames(t *testing.T) {
	commander := &fakeCommander{run: func(name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			return []byte("not-a-number\n"), nil
		}
		return nil, createLastArg(t, args, []byte("jpeg"))
	}}
	framer := newTestFramer(t, commander)
	if _, err := framer.Frames(context.Background(), "video.ts", 4); err == nil {
		t.Fatal("接受非法时长")
	}
	if _, err := framer.Frames(context.Background(), "video.ts", 6); err == nil {
		t.Fatal("接受超量抽帧")
	}

	commander.run = func(name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			return []byte("5\n"), nil
		}
		// 不生成输出文件，模拟 ffmpeg 静默失败
		return nil, nil
	}
	if _, err := framer.Frames(context.Background(), "video.ts", 1); err == nil || !strings.Contains(err.Error(), "未生成") {
		t.Fatalf("缺失帧未报错：%v", err)
	}

	commander.run = func(name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			return []byte("5\n"), nil
		}
		return nil, errors.New("ffmpeg 执行失败（[url]）：exit status 1")
	}
	if _, err := framer.Frames(context.Background(), "video.ts", 1); err == nil || !strings.Contains(err.Error(), "抽帧失败") {
		t.Fatalf("ffmpeg失败未报错：%v", err)
	}
}

func TestFFmpegFramerRejectsEmptyVideoPath(t *testing.T) {
	framer := newTestFramer(t, &fakeCommander{})
	if _, err := framer.Frames(context.Background(), "  ", 3); err == nil {
		t.Fatal("接受空视频路径")
	}
	if _, err := NewFFmpegFramer(FFmpegFramerOptions{FFprobePath: "ffprobe"}); err == nil {
		t.Fatal("接受空ffmpeg路径")
	}
}
