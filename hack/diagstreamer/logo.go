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
)

//go:embed assets/trickster-vertical.png
var logoPNG []byte

// logoSprite scales the Trickster vertical logo, mark over wordmark, to height pixels tall
func logoSprite(height int) (*sprite, error) {
	src, err := png.Decode(bytes.NewReader(logoPNG))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	lh := max(height, 8)
	lw := max(lh*b.Dx()/b.Dy(), 8)
	return newSprite(downscale(src, lw, lh)), nil
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
