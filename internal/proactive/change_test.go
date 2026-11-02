package proactive

import (
	"image"
	"image/color"
	"testing"
)

func solid(width, height int, value uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	return img
}

func TestChangePercent(t *testing.T) {
	black := solid(10, 10, 0)
	if got := ChangePercent(black, solid(10, 10, 0)); got != 0 {
		t.Errorf("相同画面变化应为0，实际 %v", got)
	}
	if got := ChangePercent(black, solid(10, 10, 5)); got != 0 {
		t.Errorf("压缩噪声不应算变化，实际 %v", got)
	}
	if got := ChangePercent(black, solid(10, 10, 255)); got != 100 {
		t.Errorf("全画面变化应为100，实际 %v", got)
	}
	half := solid(10, 10, 0)
	for y := 0; y < 10; y++ {
		for x := 0; x < 5; x++ {
			half.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	if got := ChangePercent(black, half); got != 50 {
		t.Errorf("半画面变化应为50，实际 %v", got)
	}
	if got := ChangePercent(black, solid(20, 10, 0)); got != 100 {
		t.Errorf("尺寸变化应按100处理，实际 %v", got)
	}
	if got := ChangePercent(nil, black); got != 100 {
		t.Errorf("缺少上一帧应按100处理，实际 %v", got)
	}
}
