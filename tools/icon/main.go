package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const canvasSize = 1024

type layerColor struct {
	r float64
	g float64
	b float64
	a float64
}

func main() {
	output := flag.String("output", filepath.FromSlash("build/appicon.png"), "PNG 输出路径")
	flag.Parse()

	icon := renderIcon(canvasSize)
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fatalf("创建图标目录失败：%v", err)
	}
	file, err := os.Create(*output)
	if err != nil {
		fatalf("创建图标失败：%v", err)
	}
	defer file.Close()
	if err := png.Encode(file, icon); err != nil {
		fatalf("编码图标失败：%v", err)
	}
	fmt.Printf("已生成 %s（%d×%d）\n", *output, canvasSize, canvasSize)
}

func renderIcon(size int) *image.NRGBA {
	imageData := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := float64(size) / canvasSize
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px := (float64(x) + 0.5) / scale
			py := (float64(y) + 0.5) / scale
			pixel := layerColor{}

			shapeDistance := roundedRectDistance(px, py, 512, 502, 430, 430, 210)
			coverage := smoothCoverage(shapeDistance, 1.4/scale)
			if coverage > 0 {
				t := clamp((px+py-180)/1500, 0, 1)
				base := mix(layerColor{r: 118, g: 144, b: 255, a: 1}, layerColor{r: 54, g: 84, b: 220, a: 1}, t)
				highlightDistance := math.Hypot(px-334, py-258)
				highlight := clamp(1-highlightDistance/680, 0, 1) * 0.19
				base = mix(base, layerColor{r: 255, g: 255, b: 255, a: 1}, highlight)
				base.a = coverage
				pixel = blend(pixel, base)
			}

			bars := []struct {
				centerX float64
				centerY float64
				halfW   float64
				halfH   float64
				alpha   float64
			}{
				{311, 502, 19, 81, 0.76},
				{373, 502, 19, 137, 0.86},
				{435, 502, 19, 193, 0.94},
				{512, 502, 34, 249, 1},
				{589, 502, 19, 193, 0.94},
				{651, 502, 19, 137, 0.86},
				{713, 502, 19, 81, 0.76},
			}
			for _, bar := range bars {
				barDistance := roundedRectDistance(px, py, bar.centerX, bar.centerY, bar.halfW, bar.halfH, bar.halfW)
				barCoverage := smoothCoverage(barDistance, 1.15/scale) * bar.alpha
				if barCoverage > 0 {
					pixel = blend(pixel, layerColor{r: 255, g: 255, b: 255, a: barCoverage})
				}
			}

			imageData.SetNRGBA(x, y, color.NRGBA{
				R: uint8(math.Round(clamp(pixel.r, 0, 255))),
				G: uint8(math.Round(clamp(pixel.g, 0, 255))),
				B: uint8(math.Round(clamp(pixel.b, 0, 255))),
				A: uint8(math.Round(clamp(pixel.a, 0, 1) * 255)),
			})
		}
	}
	return imageData
}

func roundedRectDistance(x, y, centerX, centerY, halfWidth, halfHeight, radius float64) float64 {
	qx := math.Abs(x-centerX) - halfWidth + radius
	qy := math.Abs(y-centerY) - halfHeight + radius
	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	inside := math.Min(math.Max(qx, qy), 0)
	return outside + inside - radius
}

func smoothCoverage(distance, width float64) float64 {
	return clamp(0.5-distance/width, 0, 1)
}

func mix(left, right layerColor, amount float64) layerColor {
	return layerColor{
		r: left.r + (right.r-left.r)*amount,
		g: left.g + (right.g-left.g)*amount,
		b: left.b + (right.b-left.b)*amount,
		a: left.a + (right.a-left.a)*amount,
	}
}

func blend(destination, source layerColor) layerColor {
	resultAlpha := source.a + destination.a*(1-source.a)
	if resultAlpha <= 0 {
		return layerColor{}
	}
	return layerColor{
		r: (source.r*source.a + destination.r*destination.a*(1-source.a)) / resultAlpha,
		g: (source.g*source.a + destination.g*destination.a*(1-source.a)) / resultAlpha,
		b: (source.b*source.a + destination.b*destination.a*(1-source.a)) / resultAlpha,
		a: resultAlpha,
	}
}

func clamp(value, minimum, maximum float64) float64 {
	return math.Max(minimum, math.Min(maximum, value))
}

func fatalf(format string, values ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", values...)
	os.Exit(1)
}
