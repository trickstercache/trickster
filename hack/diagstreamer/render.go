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
	"math"
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
	flashShare     = 10 // the flash and the beep last 1/flashShare of a second
	logoAspectY    = 0.71
	speedLines     = 720.0
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

// palette is the backgrounds the stream cycles through: dark enough for white text, distinct from each other
var palette = []ycc{
	rgb(0x1b, 0x2a, 0x41), rgb(0x3d, 0x1e, 0x6d), rgb(0x0b, 0x5d, 0x5e), rgb(0x6d, 0x1a, 0x36),
	rgb(0x1f, 0x51, 0x30), rgb(0x2e, 0x2e, 0x2e), rgb(0x7a, 0x3b, 0x0c), rgb(0x4a, 0x14, 0x5c),
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
	vx, vy       float64
	stream       string
}

// NewRenderer prepares the frame buffer, the logo and the layout for cfg
func NewRenderer(cfg Config) (*Renderer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logo, err := logoSprite(cfg.Width)
	if err != nil {
		return nil, err
	}
	r := &Renderer{
		cfg: cfg, tl: newTimeline(&cfg), c: newCanvas(cfg.Width, cfg.Height), logo: logo,
		unit: unitFor(cfg.Height), scale: cfg.Height,
	}
	if cfg.ColorPeriod > 0 {
		r.segsPerColor = int64((cfg.ColorPeriod + cfg.Segment - 1) / cfg.Segment)
	}
	r.vx = float64(cfg.LogoSpeed) * float64(cfg.Height) / speedLines
	r.vy = r.vx * logoAspectY
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

// triangle folds v into [0, span] as a point bouncing between both ends would travel
func triangle(v, span float64) float64 {
	if span <= 0 {
		return 0
	}
	m := math.Mod(v, 2*span)
	return span - math.Abs(m-span)
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
	c.fill(bg)

	_, _, _, stripHeight := stripGeometry(w, h, u)
	t := float64(n) / float64(r.tl.fps)
	lx := triangle(r.vx*t, float64(w-r.logo.w))
	ly := triangle(r.vy*t, float64(h-stripHeight-r.logo.h))

	r.panel()
	r.clock(n, at, seg)
	r.flash(n)
	if r.cfg.Captions {
		r.caption(at, h-stripHeight)
	}
	// the logo travels over everything but the timing strip, which it never reaches
	c.draw(r.logo, int(lx), int(ly))
	c.timingStrip(Timing{Frame: uint32(n), At: at}, u) //nolint:gosec // the strip carries the low 32 bits
	return c.buf
}

func (r *Renderer) fit(s string, scale, width int) string {
	for len(s) > 1 && textWidth(s, scale) > width {
		s = s[:len(s)-1]
	}
	return s
}

func (r *Renderer) panel() {
	c, w := r.c, r.cfg.Width
	m, ts, is, sh := r.px(marginPx), r.px(titlePx), r.px(infoPx), r.px(shadowPx)
	room := w - 2*m - r.px(flashPx) - m
	lines := make([]string, 0, len(r.cfg.Fields)+1)
	lines = append(lines, r.fit(r.stream, is, room))
	for _, f := range r.cfg.Fields {
		lines = append(lines, r.fit(f.Key+": "+f.Value, is, room))
	}
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
	c, w, h := r.c, r.cfg.Width, r.cfg.Height
	cs, is, sh, gap := r.px(clockPx), r.px(infoPx), r.px(shadowPx), r.px(gapPx)
	clock := at.Format(clockLayout)
	for cs > 1 && textWidth(clock, cs) > w-2*r.px(marginPx) {
		cs--
	}
	date := at.Format(dateLayout)
	info := fmt.Sprintf("FRAME %d  SEGMENT %d  TC %s", n, seg, r.timecode(n))
	cy := h/2 - glyphHeight*cs/2
	top := cy - glyphHeight*is - gap
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
