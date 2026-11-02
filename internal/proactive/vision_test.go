package proactive

// 本文件测详细识图的适配层：固定提示词、单帧、错误透传。

import (
	"context"
	"errors"
	"testing"
)

type fakeFrames struct {
	gotFrames []string
	gotPrompt string
	text      string
	err       error
}

func (f *fakeFrames) Describe(_ context.Context, frames []string, prompt string) (string, error) {
	f.gotFrames = frames
	f.gotPrompt = prompt
	return f.text, f.err
}

// TestVisionFromFramesUsesFixedPrompt 适配器必须用固定的识图提示词，且只传一帧。
func TestVisionFromFramesUsesFixedPrompt(t *testing.T) {
	inner := &fakeFrames{text: "画面里有一条鱼在啃面包。"}
	vision := VisionFromFrames(inner)
	if vision == nil {
		t.Fatal("适配器不应为 nil")
	}

	got, err := vision.Describe(context.Background(), "/tmp/frame.jpg")
	if err != nil {
		t.Fatalf("识图失败：%v", err)
	}
	if got != "画面里有一条鱼在啃面包。" {
		t.Errorf("描述 = %q", got)
	}
	if inner.gotPrompt != VisionPrompt {
		t.Errorf("提示词被改动：%q", inner.gotPrompt)
	}
	if len(inner.gotFrames) != 1 || inner.gotFrames[0] != "/tmp/frame.jpg" {
		t.Errorf("帧参数不符：%v", inner.gotFrames)
	}
	if VisionPrompt != "你是视图模型，专门用于识图，请你详细观察图片，并详细说明解释图中内容" {
		t.Errorf("识图提示词与派发单不一致：%q", VisionPrompt)
	}
}

// TestVisionFromFramesPropagatesError 底层错误原样上抛。
func TestVisionFromFramesPropagatesError(t *testing.T) {
	boom := errors.New("vision down")
	vision := VisionFromFrames(&fakeFrames{err: boom})
	if _, err := vision.Describe(context.Background(), "/tmp/x.jpg"); !errors.Is(err, boom) {
		t.Errorf("错误未透传：%v", err)
	}
}

// TestVisionFromFramesNil nil 描述器返回 nil，交给 Runner 构造期拦截。
func TestVisionFromFramesNil(t *testing.T) {
	if VisionFromFrames(nil) != nil {
		t.Error("nil 输入应返回 nil")
	}
}
