/*
 * Copyright 2026 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package diagstreamer

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/png"
	"math"
)

//go:embed assets/trickster-horizontal.png
var logoPNG []byte

const (
	logoWidthShare = 0.28 // of the frame width
	platePadShare  = 0.18 // of the scaled logo's height, on every side
	plateRadius    = 0.5  // of the padding
)

// logoSprite scales the Trickster horizontal logo to the frame and sets it on a rounded white plate, which keeps
// its blue legible over every background color
func logoSprite(frameWidth int) (*sprite, error) {
	src, err := png.Decode(bytes.NewReader(logoPNG))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	lw := max(int(float64(frameWidth)*logoWidthShare), 32)
	lh := max(lw*b.Dy()/b.Dx(), 8)
	logo := downscale(src, lw, lh)
	pad := max(int(float64(lh)*platePadShare), 2)
	pw, ph := lw+2*pad, lh+2*pad
	plate := image.NewRGBA(image.Rect(0, 0, pw, ph))
	radius := float64(pad) * plateRadius * 2
	for y := range ph {
		for x := range pw {
			a := roundedAlpha(float64(x)+0.5, float64(y)+0.5, float64(pw), float64(ph), radius)
			plate.SetRGBA(x, y, color.RGBA{a, a, a, a})
		}
	}
	for y := range lh {
		for x := range lw {
			over(plate, x+pad, y+pad, logo.RGBAAt(x, y))
		}
	}
	return newSprite(plate), nil
}

// roundedAlpha is the coverage of a rectangle of w by h with corners of radius r at the point x, y
func roundedAlpha(x, y, w, h, r float64) uint8 {
	cx := math.Max(r-x, math.Max(x-(w-r), 0))
	cy := math.Max(r-y, math.Max(y-(h-r), 0))
	d := math.Hypot(cx, cy) - r
	switch {
	case d <= -0.5:
		return 0xff
	case d >= 0.5:
		return 0
	}
	return uint8((0.5 - d) * 0xff)
}

// over composites the premultiplied color c onto dst at x, y
//
//nolint:gosec // premultiplied source over destination stays within a byte
func over(dst *image.RGBA, x, y int, c color.RGBA) {
	d := dst.RGBAAt(x, y)
	inv := 255 - int(c.A)
	dst.SetRGBA(x, y, color.RGBA{
		uint8(int(c.R) + int(d.R)*inv/255), uint8(int(c.G) + int(d.G)*inv/255), //nolint:gosec // see above
		uint8(int(c.B) + int(d.B)*inv/255), uint8(int(c.A) + int(d.A)*inv/255), //nolint:gosec // see above
	})
}

// downscale resizes src to w by h by averaging the premultiplied samples each output pixel covers
func downscale(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	sw, sh := b.Dx(), b.Dy()
	for y := range h {
		y0, y1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := range w {
			x0, x1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			var r, g, bl, a, n uint32
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r, g, bl, a, n = r+cr, g+cg, bl+cb, a+ca, n+1
				}
			}
			//nolint:gosec // means of 16-bit channels
			out.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), uint8(a / n >> 8)})
		}
	}
	return out
}
