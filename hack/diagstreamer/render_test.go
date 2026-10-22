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
	"fmt"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	c := DefaultConfig()
	c.Width, c.Height, c.FPS, c.Segment = 320, 180, 10, time.Second
	c.VideoBitrate, c.AudioBitrate, c.Window = 300_000, 64_000, 3
	c.Fields = []Field{{"Site", "lab"}}
	return c
}

func TestGlyphsCoverPrintableASCII(t *testing.T) {
	for r := rune(' '); r <= '~'; r++ {
		if _, ok := glyphBits[r]; !ok {
			t.Errorf("no glyph for %q", r)
		}
	}
	if glyph('é') != glyphBits['?'] {
		t.Error("a missing glyph draws as ?")
	}
	if textWidth("", 2) != 0 || textWidth("ab", 2) != (2*glyphAdvance-1)*2 {
		t.Error("text width")
	}
}

func TestColorRoundTrip(t *testing.T) {
	for _, c := range []color.RGBA{{0, 0, 0, 255}, {255, 255, 255, 255}, {0x33, 0x66, 0x99, 255}, {200, 30, 90, 255}} {
		got := toRGB(rgb(c.R, c.G, c.B))
		for i, pair := range [][2]uint8{{got.R, c.R}, {got.G, c.G}, {got.B, c.B}} {
			if math.Abs(float64(pair[0])-float64(pair[1])) > 3 {
				t.Errorf("%v channel %d came back %v", c, i, got)
			}
		}
	}
	if white.y != lumaWhite || black.y != lumaBlack || rgb(255, 255, 255).y != lumaWhite {
		t.Error("limited range levels")
	}
}

func TestCanvas(t *testing.T) {
	c := newCanvas(16, 8)
	c.fill(white)
	c.rect(-4, -4, 4, 4, black)
	if c.y[0] != lumaBlack || c.y[3*16+3] != lumaBlack || c.y[3*16+4] != lumaWhite || c.y[4*16] != lumaWhite {
		t.Error("rect fills its clipped area only")
	}
	c.rect(20, 20, 30, 30, black)
	c.shade(8, 0, 16, 8, 0)
	if c.y[15] != lumaBlack || c.cb[7] != chromaZero {
		t.Error("shade darkens toward black")
	}
	c.fill(gray)
	c.text(0, 0, 1, "|", white)
	if c.y[2] != lumaWhite || c.y[0] != gray.y {
		t.Error("text sets the glyph's pixels only")
	}
	img := c.image()
	if img.Bounds().Dx() != 16 || img.YStride != 16 || img.CStride != 8 {
		t.Error("image geometry")
	}
	if c.rgba().Bounds().Dy() != 8 {
		t.Error("rgba geometry")
	}
}

func TestSprite(t *testing.T) {
	logo, err := logoSprite(300)
	if err != nil {
		t.Fatal(err)
	}
	if logo.w != 300 || logo.h != 300 {
		t.Fatalf("logo %dx%d", logo.w, logo.h)
	}
	// the vertical logo is drawn straight on the background: transparent at its corners, opaque in its mark
	if logo.ya[0] != 0 || logo.ya[(logo.h/3)*logo.w+logo.w/2] == 0 {
		t.Error("logo alpha")
	}
	c := newCanvas(64, 64)
	c.fill(black)
	c.draw(logo, -logo.w/2, -logo.h/2)
	c.draw(logo, 60, 60)
	if bytes.Count(c.y, []byte{lumaBlack}) == len(c.y) {
		t.Error("the logo was drawn")
	}
}

func TestTestBars(t *testing.T) {
	r, err := NewRenderer(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	r.Render(r.FrameAt(time.Now()))
	// each bar holds its 75% color, and the reverse strip under it its own
	at := func(i, y int) uint8 { return r.c.y[y*r.cfg.Width+r.barsX+r.barsW*(2*i+1)/(2*len(topBars))] }
	for i := range topBars {
		if at(i, r.rowY+1) != topBars[i].y || at(i, r.rowY+r.rowH-1) != stripBars[i].y {
			t.Errorf("bar %d", i)
		}
	}
	if topBars[0].y != rgb(barLevel, barLevel, barLevel).y || topBars[6] == stripBars[6] {
		t.Error("bar colors")
	}
	if r.barsX <= r.logoX || r.barsX+r.barsW > r.cfg.Width {
		t.Errorf("the row sits in the frame: logo at %d, bars at %d+%d", r.logoX, r.barsX, r.barsW)
	}
}

func TestTimingStripRoundTrip(t *testing.T) {
	r, err := NewRenderer(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)
	n := r.FrameAt(at) + 7
	r.Render(n)
	got, ok := DecodeTiming(r.Image())
	if !ok || got.Frame != uint32(n) || !got.At.Equal(r.tl.at(n).Truncate(time.Millisecond)) {
		t.Fatalf("decoded %+v %v", got, ok)
	}
	// a damaged strip fails its CRC
	_, _, y0, height := stripGeometry(320, 180, unitFor(180))
	cell := 320 / stripCells
	x := (320-cell*stripCells)/2 + (stripStartBits+3)*cell + cell/2
	i := (y0+height/2)*320 + x
	if r.c.y[i] > stripThreshold {
		r.c.y[i] = lumaBlack
	} else {
		r.c.y[i] = lumaWhite
	}
	if _, ok = DecodeTiming(r.Image()); ok {
		t.Error("a flipped bit is caught")
	}
	r.c.fill(black)
	if _, ok = DecodeTiming(r.Image()); ok {
		t.Error("no start pattern, no timing")
	}
}

func TestRendererLayout(t *testing.T) {
	cfg := testConfig()
	cfg.ColorPeriod = 2 * time.Second
	r, err := NewRenderer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	base := r.FrameAt(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	background := func(n int64) uint8 {
		r.Render(n)
		return r.c.y[(cfg.Height/2-30)*cfg.Width+2]
	}
	// colors hold for a color period and change at the next one
	if background(base) != background(base+19) || background(base) == background(base+20) {
		t.Error("the background changes on color period boundaries")
	}
	if len(r.Frame()) != cfg.Width*cfg.Height*3/2 {
		t.Error("frame size")
	}
	if !flashing(base, 10) || flashing(base+1, 10) || !flashing(base, 5) {
		t.Error("the flash covers the first tenth of a second")
	}
	if got := r.timecode(base + 25); got != "00:00:02:05" {
		t.Errorf("timecode %s", got)
	}
	if formatBitrate(3_000_000) != "3.0 Mb/s" || formatBitrate(64_000) != "64 kb/s" {
		t.Error("bit rate formatting")
	}
	if r.fit("a long title", 1, textWidth("a lo", 1)) != "a lo" {
		t.Error("fit trims to the width")
	}
	if _, err = NewRenderer(Config{}); err == nil {
		t.Error("an invalid config is refused")
	}
	cfg.Captions, cfg.ColorPeriod = false, 0
	r, _ = NewRenderer(cfg)
	if background(base) != background(base+100) {
		t.Error("no color period keeps one background")
	}
	if r.RGBA().Bounds().Dx() != cfg.Width {
		t.Error("snapshot size")
	}
}

func TestTimeline(t *testing.T) {
	cfg := testConfig()
	tl := newTimeline(&cfg)
	at := time.Date(2026, 10, 20, 6, 0, 1, 300_000_000, time.UTC)
	n := tl.frameAt(at)
	if !tl.at(n).Equal(at) || tl.segment(n) != at.Unix() {
		t.Errorf("frame %d is %s in segment %d", n, tl.at(n), tl.segment(n))
	}
	if tl.sampleAt(n+1)-tl.sampleAt(n) != SampleRate/10 {
		t.Error("samples per frame")
	}
}

func TestTone(t *testing.T) {
	var tone Tone
	start := int64(1792476000) * SampleRate // a whole minute
	sample := func(b []byte, i int) int16 { return int16(uint16(b[i*4]) | uint16(b[i*4+1])<<8) }
	beep := tone.Samples(start, SampleRate/10)
	var peak int16
	for i := range SampleRate / 10 {
		peak = max(peak, sample(beep, i))
	}
	if float64(peak) < beepAmplitude*0.9 {
		t.Errorf("the beep peaks at %d", peak)
	}
	quiet := tone.Samples(start+SampleRate/2, 100)
	for i := range 100 {
		if sample(quiet, i) != 0 {
			t.Fatal("silence between beeps")
		}
	}
	crossings := func(from int64) int {
		b := tone.Samples(from, SampleRate/20)
		n := 0
		for i := 1; i < SampleRate/20; i++ {
			if (sample(b, i-1) < 0) != (sample(b, i) < 0) {
				n++
			}
		}
		return n
	}
	if minute, second := crossings(start), crossings(start+SampleRate); minute <= second {
		t.Errorf("the minute beep is higher: %d vs %d crossings", minute, second)
	}
}

func BenchmarkRenderDefaultLadder(b *testing.B) {
	cfg := DefaultConfig()
	p, err := newProducer(cfg, cfg.Ladder(), time.Now)
	if err != nil {
		b.Fatal(err)
	}
	n := p.start
	for b.Loop() {
		p.render(n)
		n++
	}
}

func TestBackgroundLabels(t *testing.T) {
	names := map[string]bool{}
	for _, s := range palette {
		if names[s.name] {
			t.Errorf("%s names two colors", s.name)
		}
		names[s.name] = true
	}
	navy := palette[0]
	if got := navy.label(); got != "Background: Navy #1B2A41  Y 51 Cb 140 Cr 120" {
		t.Errorf("label %q", got)
	}
	c := rgb(navy.r, navy.g, navy.b)
	if !strings.Contains(navy.label(), fmt.Sprintf("Y %d Cb %d Cr %d", c.y, c.cb, c.cr)) {
		t.Error("the label carries the codes the frame is filled with")
	}
}
