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
	"image"
	"image/color"
)

// BT.709 limited range: luma spans 16 to 235 and chroma 16 to 240 around 128
const (
	lumaBlack  = 16
	lumaWhite  = 235
	chromaZero = 128
	kr, kb     = 0.2126, 0.0722
	lumaSpan   = 219.0
	chromaSpan = 224.0
)

type ycc struct {
	y, cb, cr uint8
}

var (
	white = ycc{lumaWhite, chromaZero, chromaZero}
	black = ycc{lumaBlack, chromaZero, chromaZero}
	gray  = ycc{(lumaBlack + lumaWhite) / 2, chromaZero, chromaZero}
)

func rgb(r, g, b uint8) ycc {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	yf := kr*rf + (1-kr-kb)*gf + kb*bf
	cb := (bf - yf) / (2 * (1 - kb))
	cr := (rf - yf) / (2 * (1 - kr))
	return ycc{clamp8(lumaBlack + yf*lumaSpan), clamp8(chromaZero + cb*chromaSpan), clamp8(chromaZero + cr*chromaSpan)}
}

func toRGB(c ycc) color.RGBA {
	yf := (float64(c.y) - lumaBlack) / lumaSpan
	cb := (float64(c.cb) - chromaZero) / chromaSpan
	cr := (float64(c.cr) - chromaZero) / chromaSpan
	rf := yf + 2*(1-kr)*cr
	bf := yf + 2*(1-kb)*cb
	gf := (yf - kr*rf - kb*bf) / (1 - kr - kb)
	return color.RGBA{clamp8(rf * 255), clamp8(gf * 255), clamp8(bf * 255), 0xff}
}

func clamp8(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}

// canvas is one yuv420p frame: the luma plane then both chroma planes, contiguous so it is written in one call
type canvas struct {
	w, h      int
	buf       []byte
	y, cb, cr []byte
}

func newCanvas(w, h int) *canvas {
	ys, cs := w*h, (w/2)*(h/2)
	buf := make([]byte, ys+2*cs)
	return &canvas{w: w, h: h, buf: buf, y: buf[:ys], cb: buf[ys : ys+cs], cr: buf[ys+cs:]}
}

func fillBytes(b []byte, v byte) {
	if len(b) == 0 {
		return
	}
	b[0] = v
	for n := 1; n < len(b); n *= 2 {
		copy(b[n:], b[:n])
	}
}

func (c *canvas) fill(col ycc) {
	fillBytes(c.y, col.y)
	fillBytes(c.cb, col.cb)
	fillBytes(c.cr, col.cr)
}

func (c *canvas) clip(x0, y0, x1, y1 int) (int, int, int, int, bool) {
	x0, y0, x1, y1 = max(x0, 0), max(y0, 0), min(x1, c.w), min(y1, c.h)
	return x0, y0, x1, y1, x0 < x1 && y0 < y1
}

// rect fills [x0, x1) by [y0, y1); chroma samples follow the luma rows and columns they cover
func (c *canvas) rect(x0, y0, x1, y1 int, col ycc) {
	x0, y0, x1, y1, ok := c.clip(x0, y0, x1, y1)
	if !ok {
		return
	}
	for y := y0; y < y1; y++ {
		fillBytes(c.y[y*c.w+x0:y*c.w+x1], col.y)
	}
	cw := c.w / 2
	for y := y0 / 2; y < (y1+1)/2; y++ {
		fillBytes(c.cb[y*cw+x0/2:y*cw+(x1+1)/2], col.cb)
		fillBytes(c.cr[y*cw+x0/2:y*cw+(x1+1)/2], col.cr)
	}
}

// shade darkens [x0, x1) by [y0, y1) toward black, keeping keep/256 of its brightness and color
func (c *canvas) shade(x0, y0, x1, y1, keep int) {
	x0, y0, x1, y1, ok := c.clip(x0, y0, x1, y1)
	if !ok {
		return
	}
	for y := y0; y < y1; y++ {
		row := c.y[y*c.w+x0 : y*c.w+x1]
		for i, v := range row {
			row[i] = uint8(lumaBlack + (int(v)-lumaBlack)*keep/256) //nolint:gosec // between black and v
		}
	}
	cw := c.w / 2
	for _, plane := range [][]byte{c.cb, c.cr} {
		for y := y0 / 2; y < (y1+1)/2; y++ {
			row := plane[y*cw+x0/2 : y*cw+(x1+1)/2]
			for i, v := range row {
				row[i] = uint8(chromaZero + (int(v)-chromaZero)*keep/256) //nolint:gosec // between zero and v
			}
		}
	}
}

// text draws s with its top-left at x, y, each font pixel a scale-by-scale square
func (c *canvas) text(x, y, scale int, s string, col ycc) {
	for _, r := range s {
		g := glyph(r)
		for gy, bits := range g {
			for gx := range glyphWidth {
				if bits&(1<<(glyphWidth-1-gx)) != 0 {
					px, py := x+gx*scale, y+gy*scale
					c.rect(px, py, px+scale, py+scale, col)
				}
			}
		}
		x += glyphAdvance * scale
	}
}

// label draws s with a one-unit drop shadow, so it reads on any background
func (c *canvas) label(x, y, scale, shadow int, s string, col ycc) {
	c.text(x+shadow, y+shadow, scale, s, black)
	c.text(x, y, scale, s, col)
}

func (c *canvas) image() *image.YCbCr {
	return &image.YCbCr{
		Y: c.y, Cb: c.cb, Cr: c.cr, YStride: c.w, CStride: c.w / 2,
		SubsampleRatio: image.YCbCrSubsampleRatio420, Rect: image.Rect(0, 0, c.w, c.h),
	}
}

// rgba converts the frame with the BT.709 limited-range matrix it was drawn in, for snapshots
func (c *canvas) rgba() *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, c.w, c.h))
	cw := c.w / 2
	for y := range c.h {
		for x := range c.w {
			ci := (y/2)*cw + x/2
			out.SetRGBA(x, y, toRGB(ycc{c.y[y*c.w+x], c.cb[ci], c.cr[ci]}))
		}
	}
	return out
}

// sprite is an image with alpha, prepared in the frame's color space so drawing it is a blend per sample
type sprite struct {
	w, h       int
	y, ya      []byte
	cb, cr, ca []byte
	cstride    int
}

func newSprite(src image.Image) *sprite {
	b := src.Bounds()
	w, h := b.Dx()&^1, b.Dy()&^1
	cw := w / 2
	s := &sprite{
		w: w, h: h, y: make([]byte, w*h), ya: make([]byte, w*h), cb: make([]byte, cw*(h/2)),
		cr: make([]byte, cw*(h/2)), ca: make([]byte, cw*(h/2)), cstride: cw,
	}
	for y := 0; y < h; y += 2 {
		for x := 0; x < w; x += 2 {
			var sumCb, sumCr, sumA int
			for dy := range 2 {
				for dx := range 2 {
					r, g, bl, a := src.At(b.Min.X+x+dx, b.Min.Y+y+dy).RGBA()
					col, alpha := unpremultiply(r, g, bl, a)
					i := (y+dy)*w + x + dx
					s.y[i], s.ya[i] = col.y, alpha
					sumCb += int(col.cb) * int(alpha)
					sumCr += int(col.cr) * int(alpha)
					sumA += int(alpha)
				}
			}
			ci := (y/2)*cw + x/2
			s.ca[ci] = uint8(sumA / 4)
			if sumA > 0 {
				s.cb[ci], s.cr[ci] = uint8(sumCb/sumA), uint8(sumCr/sumA) //nolint:gosec // weighted means of bytes
			} else {
				s.cb[ci], s.cr[ci] = chromaZero, chromaZero
			}
		}
	}
	return s
}

func unpremultiply(r, g, b, a uint32) (ycc, uint8) {
	if a == 0 {
		return black, 0
	}
	return rgb(uint8(r*0xff/a), uint8(g*0xff/a), uint8(b*0xff/a)), uint8(a >> 8) //nolint:gosec // premultiplied, so <= a
}

func blend(dst, src, alpha uint8) uint8 {
	return uint8((int(src)*int(alpha) + int(dst)*(255-int(alpha)) + 127) / 255) //nolint:gosec // a blend of two bytes
}

// draw blends the sprite with its top-left at x, y, rounded down to even coordinates so chroma lines up
func (c *canvas) draw(s *sprite, x, y int) {
	x, y = x&^1, y&^1
	for sy := range s.h {
		dy := y + sy
		if dy < 0 || dy >= c.h {
			continue
		}
		for sx := range s.w {
			dx := x + sx
			if dx < 0 || dx >= c.w {
				continue
			}
			if a := s.ya[sy*s.w+sx]; a > 0 {
				di := dy*c.w + dx
				c.y[di] = blend(c.y[di], s.y[sy*s.w+sx], a)
			}
		}
	}
	cw := c.w / 2
	for sy := range s.h / 2 {
		dy := y/2 + sy
		if dy < 0 || dy >= c.h/2 {
			continue
		}
		for sx := range s.w / 2 {
			dx := x/2 + sx
			if dx < 0 || dx >= cw {
				continue
			}
			si := sy*s.cstride + sx
			if a := s.ca[si]; a > 0 {
				di := dy*cw + dx
				c.cb[di] = blend(c.cb[di], s.cb[si], a)
				c.cr[di] = blend(c.cr[di], s.cr[si], a)
			}
		}
	}
}
