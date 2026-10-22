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
	"encoding/binary"
	"image"
	"time"
)

// The timing strip runs along the bottom of every frame: a start pattern, then the frame number, the frame's Unix
// time in milliseconds and a CRC-8 of both, as cells of black and white wide enough to survive compression
const (
	stripStart     = 0b1010
	stripStartBits = 4
	stripFrameBits = 32
	stripMilliBits = 44
	stripCRCBits   = 8
	stripCells     = stripStartBits + stripFrameBits + stripMilliBits + stripCRCBits
	stripThreshold = (lumaBlack + lumaWhite) / 2
	crc8Poly       = 0x07
)

// Timing is what a frame's timing strip carries
type Timing struct {
	// Frame is the low 32 bits of the frame's number, counted at the stream's frame rate from the Unix epoch
	Frame uint32
	// At is the frame's presentation time, to the millisecond
	At time.Time
}

func stripCRC(frame uint32, millis uint64) uint8 {
	var b [12]byte
	binary.BigEndian.PutUint32(b[:4], frame)
	binary.BigEndian.PutUint64(b[4:], millis&(1<<stripMilliBits-1))
	var crc uint8
	for _, v := range b {
		crc ^= v
		for range 8 {
			if crc&0x80 != 0 {
				crc = crc<<1 ^ crc8Poly
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func stripBits(t Timing) []bool {
	millis := uint64(t.At.UnixMilli())
	out := make([]bool, 0, stripCells)
	put := func(v uint64, n int) {
		for i := n - 1; i >= 0; i-- {
			out = append(out, v>>i&1 == 1)
		}
	}
	put(stripStart, stripStartBits)
	put(uint64(t.Frame), stripFrameBits)
	put(millis, stripMilliBits)
	put(uint64(stripCRC(t.Frame, millis)), stripCRCBits)
	return out
}

func stripGeometry(w, h, unit int) (cell, x0, y0, height int) {
	cell = w / stripCells
	height = max(10*unit, 8)
	return cell, (w - cell*stripCells) / 2, h - height, height
}

func (c *canvas) timingStrip(t Timing, unit int) {
	cell, x0, y0, height := stripGeometry(c.w, c.h, unit)
	c.rect(0, y0, c.w, c.h, black)
	for i, on := range stripBits(t) {
		if on {
			c.rect(x0+i*cell, y0, x0+(i+1)*cell, y0+height, white)
		}
	}
}

// DecodeTiming reads the timing strip of a decoded frame, reporting false when its start pattern or CRC is wrong
func DecodeTiming(img *image.YCbCr) (Timing, bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	cell, x0, y0, height := stripGeometry(w, h, unitFor(h))
	if cell < 1 {
		return Timing{}, false
	}
	y := b.Min.Y + y0 + height/2
	read := func(from, n int) uint64 {
		var v uint64
		for i := range n {
			x := b.Min.X + x0 + (from+i)*cell + cell/2
			v <<= 1
			if img.Y[img.YOffset(x, y)] > stripThreshold {
				v |= 1
			}
		}
		return v
	}
	if read(0, stripStartBits) != stripStart {
		return Timing{}, false
	}
	frame := uint32(read(stripStartBits, stripFrameBits)) //nolint:gosec // 32 bits read
	millis := read(stripStartBits+stripFrameBits, stripMilliBits)
	crc := uint8(read(stripStartBits+stripFrameBits+stripMilliBits, stripCRCBits)) //nolint:gosec // 8 bits read
	if crc != stripCRC(frame, millis) {
		return Timing{}, false
	}
	return Timing{Frame: frame, At: time.UnixMilli(int64(millis))}, true //nolint:gosec // 44 bits read
}
