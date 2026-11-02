package proactive

import (
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
)

// pixelDiffThreshold 是单通道 8bit 差值容忍度；低于它的变化视为压缩噪声。
const pixelDiffThreshold = 10

// loadImage 解码截帧文件；调用方负责删除原文件。
func loadImage(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("截帧文件不可读")
	}
	defer func() { _ = file.Close() }()
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, errors.New("截帧不是可识别的图片")
	}
	return img, nil
}

// ChangePercent 返回颜色变化超过阈值的像素百分比；尺寸变化按 100% 处理。
func ChangePercent(previous, current image.Image) float64 {
	if previous == nil || current == nil {
		return 100
	}
	first, second := previous.Bounds(), current.Bounds()
	if first.Dx() != second.Dx() || first.Dy() != second.Dy() {
		return 100
	}
	total := first.Dx() * first.Dy()
	if total <= 0 {
		return 100
	}
	changed := 0
	for y := 0; y < second.Dy(); y++ {
		for x := 0; x < second.Dx(); x++ {
			ar, ag, ab, _ := previous.At(first.Min.X+x, first.Min.Y+y).RGBA()
			br, bg, bb, _ := current.At(second.Min.X+x, second.Min.Y+y).RGBA()
			if channelDiff(ar, br) >= pixelDiffThreshold ||
				channelDiff(ag, bg) >= pixelDiffThreshold ||
				channelDiff(ab, bb) >= pixelDiffThreshold {
				changed++
			}
		}
	}
	return float64(changed) * 100 / float64(total)
}

func channelDiff(a, b uint32) uint32 {
	a, b = a>>8, b>>8
	if a > b {
		return a - b
	}
	return b - a
}
