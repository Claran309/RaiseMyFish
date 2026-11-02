package watch

import (
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
)

// 亮度阈值按 0–255 的平均亮度估算：过低视为黑屏，过高视为过曝。
const (
	DarkLumaThreshold   = 16.0
	BrightLumaThreshold = 235.0
)

// CheckBrightness 解码抽到的帧并返回告警文案；正常返回空串。
// 解码失败是错误（说明帧不是真实图片），不是「无告警」。
func CheckBrightness(frames []string) (string, error) {
	if len(frames) == 0 {
		return "", errors.New("亮度检测缺少帧")
	}
	var total float64
	for _, path := range frames {
		file, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("亮度检测打开帧失败：%s", path)
		}
		img, _, decodeErr := image.Decode(file)
		closeErr := file.Close()
		if decodeErr != nil || closeErr != nil {
			return "", fmt.Errorf("亮度检测解码帧失败：%s", path)
		}
		total += averageLuma(img)
	}
	avg := total / float64(len(frames))
	switch {
	case avg < DarkLumaThreshold:
		return fmt.Sprintf("画面可能黑屏（平均亮度 %.1f/255）", avg), nil
	case avg > BrightLumaThreshold:
		return fmt.Sprintf("画面可能过曝（平均亮度 %.1f/255）", avg), nil
	default:
		return "", nil
	}
}

func averageLuma(img image.Image) float64 {
	bounds := img.Bounds()
	if bounds.Empty() {
		return 0
	}
	var sum float64
	var count int
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			sum += 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
