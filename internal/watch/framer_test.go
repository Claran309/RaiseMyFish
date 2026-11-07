package watch

import (
	"context"
	"errors"
	"os"
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

// TestFFmpegFramerCleansUpWhenLaterFrameFails O06：第二帧失败后零残留。
//
// 回归背景：旧实现失败时 return nil, err，调用方（Pipeline）拿不到帧路径，
// cleanup 里 `len(clip.Frames) > 0` 分支不执行 —— 已生成的帧和目录就漏在磁盘上。
// 现在「所有权移交前失败」由创建者自己 RemoveAll。
func TestFFmpegFramerCleansUpWhenLaterFrameFails(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, call int, args []string) ([]byte, error)
	}{
		{
			name: "第二帧命令失败",
			run: func(t *testing.T, call int, args []string) ([]byte, error) {
				if call >= 2 {
					return nil, errors.New("ffmpeg 第二帧失败")
				}
				return nil, createLastArg(t, args, []byte("jpeg"))
			},
		},
		{
			name: "第二帧未生成",
			run: func(t *testing.T, call int, args []string) ([]byte, error) {
				if call >= 2 {
					return nil, nil // 静默失败：不写文件
				}
				return nil, createLastArg(t, args, []byte("jpeg"))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			ffmpegCalls := 0
			commander := &fakeCommander{run: func(name string, args ...string) ([]byte, error) {
				if name == "ffprobe" {
					return []byte("10\n"), nil
				}
				ffmpegCalls++
				return tc.run(t, ffmpegCalls, args)
			}}
			framer, err := NewFFmpegFramer(FFmpegFramerOptions{
				FFmpegPath:  "ffmpeg",
				FFprobePath: "ffprobe",
				TempDir:     tempDir,
				Command:     commander,
			})
			if err != nil {
				t.Fatal(err)
			}

			frames, err := framer.Frames(context.Background(), "video.ts", 3)
			if err == nil {
				t.Fatal("第二帧失败应返回错误")
			}
			if frames != nil {
				t.Fatalf("失败时不应返回部分帧：%v", frames)
			}
			// 零残留：临时目录里既没有帧目录，也没有散落的 jpg。
			entries, readErr := os.ReadDir(tempDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				names := make([]string, 0, len(entries))
				for _, entry := range entries {
					names = append(names, entry.Name())
				}
				t.Fatalf("失败后仍有残留：%v", names)
			}
		})
	}
}

// TestFFmpegFramerKeepsFramesOnSuccess 成功路径不删目录（所有权已移交，由 Pipeline 清理）。
func TestFFmpegFramerKeepsFramesOnSuccess(t *testing.T) {
	tempDir := t.TempDir()
	commander := &fakeCommander{run: func(name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			return []byte("10\n"), nil
		}
		return nil, createLastArg(t, args, []byte("jpeg"))
	}}
	framer, err := NewFFmpegFramer(FFmpegFramerOptions{
		FFmpegPath: "ffmpeg", FFprobePath: "ffprobe", TempDir: tempDir, Command: commander,
	})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := framer.Frames(context.Background(), "video.ts", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 {
		t.Fatalf("帧数=%d", len(frames))
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("成功时应保留帧目录供调用方清理，实际 %d 项", len(entries))
	}
	for _, frame := range frames {
		if !fileExists(frame) {
			t.Fatalf("成功路径的帧被误删：%s", frame)
		}
	}
}
