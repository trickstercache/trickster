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
	"fmt"
	"image"
	"time"
)

const (
	unitLines      = 360 // the timing strip's unit: one per this many lines
	layoutLines    = 720 // layout sizes below are pixels at this many lines, scaled with the height
	marginPx       = 16
	titlePx        = 5 // font pixel sizes
	infoPx         = 3
	clockPx        = 10
	shadowPx       = 2
	flashPx        = 40
	panelPadPx     = 8
	panelKeep      = 96 // of 256: how much of the background shows through a panel
	sweepPx        = 6
	gapPx          = 6
	flashShare     = 10  // the flash and the beep last 1/flashShare of a second
	rowTopPx       = 150 // the logo and the test bars sit side by side in a row this far down, this tall
	rowPx          = 300
	rowGapPx       = 24
	clockTopPx     = 480 // the clock block starts this far down, below the row
	barsWide       = 16  // the test bars are barsWide by barsHigh
	barsHigh       = 9
	barsStripShare = 8 // the reverse bars along the bottom are 1/barsStripShare of the bars' height
	clockLayout    = "15:04:05.000"
	dateLayout     = "Mon 2006-01-02 MST"
	secondsPerDay  = 86400
	nanosPerSecond = int64(time.Second)
)

// timeline counts frames from the Unix epoch at the stream's frame rate, so frame and segment numbers follow the
// wall clock and a restarted generator numbers a moment as it did before
type timeline struct {
	fps, perSegment, perChunk int64
}

func newTimeline(cfg *Config) timeline {
	return timeline{fps: int64(cfg.FPS), perSegment: cfg.FramesPerSegment(), perChunk: cfg.FramesPerChunk()}
}

func (t timeline) at(n int64) time.Time {
	return time.Unix(n/t.fps, (n%t.fps)*nanosPerSecond/t.fps).UTC()
}

// frameAt is the number of the frame showing at at
func (t timeline) frameAt(at time.Time) int64 {
	return at.Unix()*t.fps + int64(at.Nanosecond())*t.fps/nanosPerSecond
}

func (t timeline) segment(n int64) int64 {
	return n / t.perSegment
}

// swatch is a named background color
type swatch struct {
	name    string
	r, g, b uint8
}

// label names the color with its RGB value and the BT.709 limited-range codes the encoder is given, so equipment
// downstream can be checked against them
func (s swatch) label() string {
	c := rgb(s.r, s.g, s.b)
	return fmt.Sprintf("Background: %s #%02X%02X%02X  Y %d Cb %d Cr %d", s.name, s.r, s.g, s.b, c.y, c.cb, c.cr)
}

// palette is the backgrounds the stream cycles through: dark enough for white text, distinct from each other
var palette = []swatch{
	{"Navy", 0x1b, 0x2a, 0x41},
	{"Indigo", 0x3d, 0x1e, 0x6d},
	{"Teal", 0x0b, 0x5d, 0x5e},
	{"Maroon", 0x6d, 0x1a, 0x36},
	{"Forest", 0x1f, 0x51, 0x30},
	{"Charcoal", 0x2e, 0x2e, 0x2e},
	{"Rust", 0x7a, 0x3b, 0x0c},
	{"Plum", 0x4a, 0x14, 0x5c},
}

func unitFor(height int) int {
	return max(height/unitLines, 1)
}

// Renderer draws the stream's frames; a frame is valid until the next call to Render
type Renderer struct {
	cfg          Config
	tl           timeline
	c            *canvas
	logo         *sprite
	unit         int
	scale        int
	segsPerColor int64
	logoX, rowY  int
	barsX, barsW int
	rowH         int
	stream       string
}

// NewRenderer prepares the frame buffer, the logo and the layout for cfg
func NewRenderer(cfg Config) (*Renderer, error) {
	// a ladder renders its top rung
	cfg = cfg.normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	r := &Renderer{
		cfg: cfg, tl: newTimeline(&cfg), c: newCanvas(cfg.Width, cfg.Height), unit: unitFor(cfg.Height),
		scale: cfg.Height,
	}
	// the static logo and the test bars form one row, centered across the frame
	r.rowH, r.rowY = r.px(rowPx), r.px(rowTopPx)
	logo, err := logoSprite(r.rowH)
	if err != nil {
		return nil, err
	}
	r.logo, r.barsW = logo, r.rowH*barsWide/barsHigh
	gap := r.px(rowGapPx)
	r.logoX = (cfg.Width - (logo.w + gap + r.barsW)) / 2
	r.barsX = r.logoX + logo.w + gap
	if cfg.ColorPeriod > 0 {
		r.segsPerColor = int64((cfg.ColorPeriod + cfg.Segment - 1) / cfg.Segment)
	}
	audio := "no audio"
	if cfg.Audio {
		audio = "AAC " + formatBitrate(cfg.AudioBitrate)
	}
	r.stream = fmt.Sprintf("%dx%d %dfps  H.264 %s  %s  %s segments", cfg.Width, cfg.Height, cfg.FPS,
		formatBitrate(cfg.VideoBitrate), audio, cfg.Segment)
	return r, nil
}

func formatBitrate(bps int) string {
	if bps >= 1_000_000 {
		return fmt.Sprintf("%.1f Mb/s", float64(bps)/1e6)
	}
	return fmt.Sprintf("%d kb/s", bps/1000)
}

// px scales a size in pixels at 720 lines to the frame's height, never below one pixel
func (r *Renderer) px(v int) int {
	return max(v*r.scale/layoutLines, 1)
}

// Frame is the yuv420p bytes of the last frame rendered
func (r *Renderer) Frame() []byte {
	return r.c.buf
}

// Image is the last frame rendered
func (r *Renderer) Image() *image.YCbCr {
	return r.c.image()
}

// RGBA is the last frame rendered, converted for viewing
func (r *Renderer) RGBA() *image.RGBA {
	return r.c.rgba()
}

// Render draws frame n, counted from the Unix epoch at the stream's frame rate, and returns its yuv420p bytes
func (r *Renderer) Render(n int64) []byte {
	c, u, w, h := r.c, r.unit, r.cfg.Width, r.cfg.Height
	at := r.tl.at(n)
	seg := r.tl.segment(n)
	bg := palette[0]
	if r.segsPerColor > 0 {
		bg = palette[(seg/r.segsPerColor)%int64(len(palette))]
	}
	c.fill(rgb(bg.r, bg.g, bg.b))

	_, _, _, stripHeight := stripGeometry(w, h, u)
	r.panel(bg)
	c.draw(r.logo, r.logoX, r.rowY)
	r.testBars()
	r.clock(n, at, seg)
	r.flash(n)
	if r.cfg.Captions {
		r.caption(at, h-stripHeight)
	}
	c.timingStrip(Timing{Frame: uint32(n), At: at}, u) //nolint:gosec // the strip carries the low 32 bits
	return c.buf
}

func (r *Renderer) fit(s string, scale, width int) string {
	for len(s) > 1 && textWidth(s, scale) > width {
		s = s[:len(s)-1]
	}
	return s
}

func (r *Renderer) panel(bg swatch) {
	c, w := r.c, r.cfg.Width
	m, ts, is, sh := r.px(marginPx), r.px(titlePx), r.px(infoPx), r.px(shadowPx)
	room := w - 2*m - r.px(flashPx) - m
	lines := make([]string, 0, len(r.cfg.Fields)+2)
	lines = append(lines, r.fit(r.stream, is, room))
	for _, f := range r.cfg.Fields {
		lines = append(lines, r.fit(f.Key+": "+f.Value, is, room))
	}
	lines = append(lines, r.fit(bg.label(), is, room))
	title := r.fit(r.cfg.Title, ts, room)
	width := textWidth(title, ts)
	for _, line := range lines {
		width = max(width, textWidth(line, is))
	}
	lineHeight := lineAdvance * is
	top := m + lineAdvance*ts
	pad := r.px(panelPadPx)
	c.shade(m-pad, m-pad, m+width+pad, top+len(lines)*lineHeight+pad, panelKeep)
	c.label(m, m, ts, sh, title, white)
	for i, line := range lines {
		c.label(m, top+i*lineHeight, is, sh, line, white)
	}
}

func (r *Renderer) clock(n int64, at time.Time, seg int64) {
	c, w := r.c, r.cfg.Width
	cs, is, sh, gap := r.px(clockPx), r.px(infoPx), r.px(shadowPx), r.px(gapPx)
	clock := at.Format(clockLayout)
	for cs > 1 && textWidth(clock, cs) > w-2*r.px(marginPx) {
		cs--
	}
	date := at.Format(dateLayout)
	info := fmt.Sprintf("FRAME %d  SEGMENT %d  TC %s", n, seg, r.timecode(n))
	top := r.px(clockTopPx)
	cy := top + glyphHeight*is + gap
	bottom := cy + glyphHeight*cs + gap
	sweepTop := bottom + glyphHeight*is + gap
	pad := r.px(panelPadPx)
	half := max(textWidth(clock, cs), textWidth(info, is)) / 2
	c.shade(w/2-half-pad, top-pad, w/2+half+pad, sweepTop+r.px(sweepPx)+pad, panelKeep)
	c.label(w/2-textWidth(date, is)/2, top, is, sh, date, white)
	c.label(w/2-textWidth(clock, cs)/2, cy, cs, sh, clock, white)
	c.label(w/2-textWidth(info, is)/2, bottom, is, sh, info, white)
	// a bar filling once a second shows motion is smooth and frames are not dropped
	frac := float64(n%r.tl.fps+1) / float64(r.tl.fps)
	c.rect(w/2-half, sweepTop, w/2+half, sweepTop+r.px(sweepPx), gray)
	c.rect(w/2-half, sweepTop, w/2-half+int(frac*float64(2*half)), sweepTop+r.px(sweepPx), white)
}

// timecode is the frame's time of day as HH:MM:SS:FF
func (r *Renderer) timecode(n int64) string {
	inDay := n % (secondsPerDay * r.tl.fps)
	s := inDay / r.tl.fps
	return fmt.Sprintf("%02d:%02d:%02d:%02d", s/3600, s/60%60, s%60, inDay%r.tl.fps)
}

// flash lights a box for the first tenth of each second, in step with the audio's beep
func (r *Renderer) flash(n int64) {
	m, size, b := r.px(marginPx), r.px(flashPx), r.px(shadowPx)
	x, y := r.cfg.Width-m-size, m
	col := gray
	if flashing(n, r.tl.fps) {
		col = white
	}
	r.c.rect(x-b, y-b, x+size+b, y+size+b, black)
	r.c.rect(x, y, x+size, y+size, col)
}

func flashing(n, fps int64) bool {
	return n%fps < max(fps/flashShare, 1)
}

func (r *Renderer) caption(at time.Time, bottom int) {
	c, w := r.c, r.cfg.Width
	is, pad := r.px(infoPx), r.px(gapPx)
	text := r.fit(captionText(r.cfg.Title, at), is, w-2*r.px(marginPx))
	tw := textWidth(text, is)
	y := bottom - 2*pad - glyphHeight*is
	c.rect(w/2-tw/2-pad, y-pad, w/2+tw/2+pad, y+glyphHeight*is+pad, black)
	c.text(w/2-tw/2, y, is, text, white)
}

// captionText is the caption shown during the second holding at, burned in and carried by the caption tracks
func captionText(title string, at time.Time) string {
	return at.Truncate(time.Second).Format(time.TimeOnly) + " UTC - " + title
}

// FrameAt is the number of the frame showing at at
func (r *Renderer) FrameAt(at time.Time) int64 {
	return r.tl.frameAt(at)
}

// barLevel makes 75% color bars, as in SMPTE EG 1: each component is 0 or 191 of 255
const barLevel = 191

var (
	topBars = []ycc{
		rgb(barLevel, barLevel, barLevel), rgb(barLevel, barLevel, 0), rgb(0, barLevel, barLevel), rgb(0, barLevel, 0),
		rgb(barLevel, 0, barLevel), rgb(barLevel, 0, 0), rgb(0, 0, barLevel),
	}
	// the reverse bars under them: blue, black, magenta, black, cyan, black, gray
	stripBars = []ycc{
		rgb(0, 0, barLevel), black, rgb(barLevel, 0, barLevel), black, rgb(0, barLevel, barLevel), black,
		rgb(barLevel, barLevel, barLevel),
	}
)

// testBars draws the classic color bars: seven 75% bars over a strip of the reverse bars, which show a hue,
// saturation or level error downstream at a glance
func (r *Renderer) testBars() {
	strip := max(r.rowH/barsStripShare, 2)
	main := r.rowH - strip
	for i := range topBars {
		x0 := r.barsX + r.barsW*i/len(topBars)
		x1 := r.barsX + r.barsW*(i+1)/len(topBars)
		r.c.rect(x0, r.rowY, x1, r.rowY+main, topBars[i])
		r.c.rect(x0, r.rowY+main, x1, r.rowY+r.rowH, stripBars[i])
	}
}
