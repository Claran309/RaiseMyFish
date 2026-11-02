package watch

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
)

func writeSolidPNG(t *testing.T, path string, shade uint8) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: shade, G: shade, B: shade, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckBrightnessWarnsDarkAndBright(t *testing.T) {
	dark := tempPath(t, "dark.png")
	bright := tempPath(t, "bright.png")
	normal := tempPath(t, "normal.png")
	writeSolidPNG(t, dark, 0)
	writeSolidPNG(t, bright, 255)
	writeSolidPNG(t, normal, 128)

	if warning, err := CheckBrightness([]string{dark}); err != nil || !strings.Contains(warning, "黑屏") {
		t.Fatalf("黑屏告警=%q err=%v", warning, err)
	}
	if warning, err := CheckBrightness([]string{bright}); err != nil || !strings.Contains(warning, "过曝") {
		t.Fatalf("过曝告警=%q err=%v", warning, err)
	}
	if warning, err := CheckBrightness([]string{normal}); err != nil || warning != "" {
		t.Fatalf("正常画面不应告警：%q %v", warning, err)
	}
}

func TestCheckBrightnessRejectsFakeFrames(t *testing.T) {
	if _, err := CheckBrightness(nil); err == nil {
		t.Fatal("接受空帧")
	}
	fake := tempPath(t, "fake.jpg")
	writeTestFile(t, fake, []byte("not-an-image"))
	if _, err := CheckBrightness([]string{fake}); err == nil {
		t.Fatal("把非图片当帧接受")
	}
}
