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

// Package diagstreamer renders a synthetic diagnostic video and audio feed, encodes it with FFmpeg, and packages it
// as live HLS, live DASH, fragmented MP4 or MoQ, for testing media delivery without media files.
package diagstreamer

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Outputs a Streamer can produce
const (
	OutputHTTP = "http"
	OutputFMP4 = "fmp4"
	OutputMoQ  = "moq"
)

// Catalog formats the moq output publishes
const (
	// CatalogMSF is the MOQT Streaming Format catalog, with each CMAF chunk an object
	CatalogMSF = "msf"
	// CatalogHang is moq-dev's hang catalog and frame container, which its web player plays
	CatalogHang = "hang"
)

// Environment variables ConfigFromEnv reads, each overriding the matching Config field
const (
	EnvWidth        = "DIAG_WIDTH"
	EnvHeight       = "DIAG_HEIGHT"
	EnvFPS          = "DIAG_FPS"
	EnvSegment      = "DIAG_SEGMENT"
	EnvChunk        = "DIAG_CHUNK"
	EnvVideoBitrate = "DIAG_VIDEO_BITRATE"
	EnvAudio        = "DIAG_AUDIO"
	EnvAudioBitrate = "DIAG_AUDIO_BITRATE"
	EnvTitle        = "DIAG_TITLE"
	EnvFields       = "DIAG_FIELDS"
	EnvCaptions     = "DIAG_CAPTIONS"
	EnvColorPeriod  = "DIAG_COLOR_PERIOD"
	EnvWindow       = "DIAG_WINDOW"
	EnvListen       = "DIAG_LISTEN"
	EnvOutput       = "DIAG_OUT"
	EnvMoQURL       = "DIAG_MOQ_URL"
	EnvFFmpeg       = "DIAG_FFMPEG"
	EnvRenditions   = "DIAG_RENDITIONS"
	EnvMoQCatalog   = "DIAG_MOQ_CATALOG"
	EnvMoQBroadcast = "DIAG_MOQ_BROADCAST"
	EnvMoQInsecure  = "DIAG_MOQ_INSECURE"
	EnvMoQCA        = "DIAG_MOQ_CA"
)

const (
	defaultWidth        = 1920
	defaultHeight       = 1080
	defaultFPS          = 30
	defaultSegment      = 2 * time.Second
	defaultVideoBitrate = 6_000_000
	defaultAudioBitrate = 128_000
	defaultTitle        = "Trickster Diagnostic Stream"
	defaultColorPeriod  = 10 * time.Second
	defaultWindow       = 10
	defaultListen       = ":8496"
	defaultFFmpeg       = "ffmpeg"
	defaultBroadcast    = "diagstreamer"

	minDimension = 128
	maxDimension = 7680
	maxFPS       = 120
	minWindow    = 3
	fieldSep     = ","
	keyValueSep  = "="
	sizeSep      = "x"
	rateSep      = "@"
	maxRungs     = 8
)

var lowerRungs = []Rendition{
	{Width: 1280, Height: 720, Bitrate: 3_000_000},
	{Width: 854, Height: 480, Bitrate: 1_500_000},
	{Width: 640, Height: 360, Bitrate: 800_000},
}

// Rendition is one rung of the video ladder
type Rendition struct {
	Width, Height int
	// Bitrate is in bits per second
	Bitrate int
}

// Name is the rung's label, such as 720p
func (r Rendition) Name() string {
	return strconv.Itoa(r.Height) + "p"
}

// String renders the rung as WxH@bitrate
func (r Rendition) String() string {
	return fmt.Sprintf("%d%s%d%s%d", r.Width, sizeSep, r.Height, rateSep, r.Bitrate)
}

// ParseRenditions reads a comma-separated ladder of WxH@bitrate rungs, such as 1920x1080@6M,1280x720@3M
func ParseRenditions(s string) ([]Rendition, error) {
	var out []Rendition
	for part := range strings.SplitSeq(s, fieldSep) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		size, rate, ok := strings.Cut(part, rateSep)
		ws, hs, ok2 := strings.Cut(size, sizeSep)
		if !ok || !ok2 {
			return nil, fmt.Errorf("rendition %q is not WxH@bitrate", part)
		}
		w, err := strconv.Atoi(ws)
		if err != nil {
			return nil, fmt.Errorf("rendition %q: %w", part, err)
		}
		h, err := strconv.Atoi(hs)
		if err != nil {
			return nil, fmt.Errorf("rendition %q: %w", part, err)
		}
		b, err := ParseBitrate(rate)
		if err != nil {
			return nil, fmt.Errorf("rendition %q: %w", part, err)
		}
		out = append(out, Rendition{Width: w, Height: h, Bitrate: b})
	}
	return out, nil
}

// Field is one key and value rendered into the stream's information panel
type Field struct {
	Key, Value string
}

// String renders the field as key=value
func (f Field) String() string {
	return f.Key + keyValueSep + f.Value
}

// ParseField reads a key=value pair
func ParseField(s string) (Field, error) {
	k, v, ok := strings.Cut(s, keyValueSep)
	k = strings.TrimSpace(k)
	if !ok || k == "" {
		return Field{}, fmt.Errorf("field %q is not key=value", s)
	}
	return Field{Key: k, Value: strings.TrimSpace(v)}, nil
}

// Config describes the stream a Streamer generates
type Config struct {
	// Width, Height and FPS are the top rendition's size and the whole-number frame rate; a Renditions ladder
	// sets the size from its top rung
	Width, Height, FPS int
	// Segment is the segment and GOP length; Chunk is the CMAF chunk length, where 0 means one frame
	Segment, Chunk time.Duration
	// VideoBitrate and AudioBitrate are in bits per second
	VideoBitrate, AudioBitrate int
	// Audio adds a tone track that beeps once a second; Captions adds caption tracks and a burned-in caption line
	Audio, Captions bool
	// Title and Fields are rendered into the information panel
	Title  string
	Fields []Field
	// ColorPeriod is how long each background color lasts, rounded up to whole segments
	ColorPeriod time.Duration
	// Window is how many segments a live manifest lists
	Window int
	// Listen is the HTTP address the http output serves on
	Listen string
	// Output is http, fmp4 (a muxed fragmented MP4 on stdout) or moq
	Output string
	// MoQURL is the relay the moq output publishes to, moqt:// for native QUIC or https:// for WebTransport
	MoQURL string
	// MoQCatalog is the catalog format the broadcast name's suffix advertises, msf or hang; both catalogs are served
	MoQCatalog string
	// MoQBroadcast names the broadcast the moq output publishes, by default diagstreamer with MoQCatalog's suffix
	MoQBroadcast string
	// MoQInsecure skips verifying the relay's certificate; MoQCA names a PEM bundle trusted for it
	MoQInsecure bool
	MoQCA       string
	// FFmpeg is the path of the ffmpeg binary
	FFmpeg string
	// Renditions is the whole video ladder, largest first; empty means the top rung of Width, Height and
	// VideoBitrate over the default lower rungs smaller than it
	Renditions []Rendition
}

// Ladder is the video ladder the stream encodes, largest first
func (c *Config) Ladder() []Rendition {
	if len(c.Renditions) > 0 {
		return c.Renditions
	}
	// the default lower rungs join the top one where they are smaller than it
	out := []Rendition{{Width: c.Width, Height: c.Height, Bitrate: c.VideoBitrate}}
	for _, r := range lowerRungs {
		if r.Height < c.Height && r.Width < c.Width {
			out = append(out, r)
		}
	}
	return out
}

func (c Config) forRendition(r Rendition) Config {
	// one rung's configuration: the stream's, at the rung's size and bit rate
	c.Width, c.Height, c.VideoBitrate = r.Width, r.Height, r.Bitrate
	c.Renditions = nil
	return c
}

func (c Config) normalized() Config {
	// a ladder that is given names the top rung, which Width, Height and VideoBitrate then describe
	if len(c.Renditions) > 0 {
		top := c.Renditions[0]
		c.Width, c.Height, c.VideoBitrate = top.Width, top.Height, top.Bitrate
	}
	return c
}

// DefaultConfig returns a 720p30 stream with audio and captions, served over HTTP
func DefaultConfig() Config {
	return Config{
		Width: defaultWidth, Height: defaultHeight, FPS: defaultFPS, Segment: defaultSegment,
		VideoBitrate: defaultVideoBitrate, AudioBitrate: defaultAudioBitrate, Audio: true, Captions: true,
		Title: defaultTitle, ColorPeriod: defaultColorPeriod, Window: defaultWindow,
		Listen: defaultListen, Output: OutputHTTP, FFmpeg: defaultFFmpeg, MoQCatalog: CatalogMSF,
	}
}

// ConfigFromEnv applies the DIAG_* environment variables that are set over base
func ConfigFromEnv(base Config) (Config, error) {
	return configFrom(base, os.LookupEnv)
}

func configFrom(c Config, lookup func(string) (string, bool)) (Config, error) {
	var errs []error
	str := func(name string, dst *string) {
		if v, ok := lookup(name); ok {
			*dst = v
		}
	}
	parse := func(name string, set func(string) error) {
		if v, ok := lookup(name); ok {
			if err := set(v); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
	}
	parse(EnvWidth, intSetter(&c.Width))
	parse(EnvHeight, intSetter(&c.Height))
	parse(EnvFPS, intSetter(&c.FPS))
	parse(EnvSegment, durationSetter(&c.Segment))
	parse(EnvChunk, durationSetter(&c.Chunk))
	parse(EnvVideoBitrate, bitrateSetter(&c.VideoBitrate))
	parse(EnvAudio, boolSetter(&c.Audio))
	parse(EnvAudioBitrate, bitrateSetter(&c.AudioBitrate))
	str(EnvTitle, &c.Title)
	parse(EnvFields, func(v string) error {
		fields, err := ParseFields(v)
		c.Fields = fields
		return err
	})
	parse(EnvCaptions, boolSetter(&c.Captions))
	parse(EnvColorPeriod, durationSetter(&c.ColorPeriod))
	parse(EnvWindow, intSetter(&c.Window))
	str(EnvListen, &c.Listen)
	str(EnvOutput, &c.Output)
	str(EnvMoQURL, &c.MoQURL)
	str(EnvFFmpeg, &c.FFmpeg)
	str(EnvMoQCatalog, &c.MoQCatalog)
	str(EnvMoQBroadcast, &c.MoQBroadcast)
	parse(EnvMoQInsecure, boolSetter(&c.MoQInsecure))
	str(EnvMoQCA, &c.MoQCA)
	parse(EnvRenditions, func(v string) (err error) {
		c.Renditions, err = ParseRenditions(v)
		return err
	})
	return c, errors.Join(errs...)
}

// ParseFields reads a comma-separated list of key=value pairs
func ParseFields(s string) ([]Field, error) {
	var out []Field
	for part := range strings.SplitSeq(s, fieldSep) {
		if strings.TrimSpace(part) == "" {
			continue
		}
		f, err := ParseField(part)
		if err != nil {
			return out, err
		}
		out = append(out, f)
	}
	return out, nil
}

// ParseBitrate reads a bit rate such as 3M, 128k or 800000
func ParseBitrate(s string) (int, error) {
	s = strings.TrimSpace(s)
	mult := 1
	switch {
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "K"):
		mult, s = 1_000, s[:len(s)-1]
	case strings.HasSuffix(s, "m"), strings.HasSuffix(s, "M"):
		mult, s = 1_000_000, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("bit rate %q is not a positive number with an optional k or M", s)
	}
	return int(v * float64(mult)), nil
}

func intSetter(dst *int) func(string) error {
	return func(v string) (err error) {
		*dst, err = strconv.Atoi(strings.TrimSpace(v))
		return err
	}
}

func boolSetter(dst *bool) func(string) error {
	return func(v string) (err error) {
		*dst, err = strconv.ParseBool(strings.TrimSpace(v))
		return err
	}
}

func durationSetter(dst *time.Duration) func(string) error {
	return func(v string) (err error) {
		*dst, err = time.ParseDuration(strings.TrimSpace(v))
		return err
	}
}

func bitrateSetter(dst *int) func(string) error {
	return func(v string) (err error) {
		*dst, err = ParseBitrate(v)
		return err
	}
}

// Validate refuses a configuration the generator cannot produce
func (c *Config) Validate() error {
	n := c.normalized()
	return n.validate()
}

func (c *Config) validate() error {
	wholeFrames := func(d time.Duration) bool { return int64(d)*int64(c.FPS)%int64(time.Second) == 0 }
	switch {
	case c.Width < minDimension || c.Height < minDimension || c.Width > maxDimension || c.Height > maxDimension:
		return fmt.Errorf("size %dx%d must be between %d and %d on each side", c.Width, c.Height, minDimension,
			maxDimension)
	case c.Width%2 != 0 || c.Height%2 != 0:
		return fmt.Errorf("size %dx%d must be even on each side for 4:2:0 video", c.Width, c.Height)
	case c.FPS < 1 || c.FPS > maxFPS:
		return fmt.Errorf("frame rate %d must be between 1 and %d", c.FPS, maxFPS)
	case c.Segment < time.Second || !wholeFrames(c.Segment):
		return fmt.Errorf("segment %s must be at least 1s and a whole number of frames", c.Segment)
	case c.Chunk < 0 || (c.Chunk > 0 && (!wholeFrames(c.Chunk) || c.Segment%c.Chunk != 0)):
		return fmt.Errorf("chunk %s must be a whole number of frames that divides the segment", c.Chunk)
	case c.VideoBitrate <= 0 || (c.Audio && c.AudioBitrate <= 0):
		return errors.New("bit rates must be positive")
	case c.ColorPeriod < 0:
		return errors.New("the color period must not be negative")
	case c.Captions && c.Segment%time.Second != 0:
		return fmt.Errorf("segment %s must be whole seconds for the once-a-second captions", c.Segment)
	case c.Window < minWindow:
		return fmt.Errorf("window %d must be at least %d segments", c.Window, minWindow)
	}
	if err := c.validateLadder(); err != nil {
		return err
	}
	switch c.Output {
	case OutputHTTP, OutputFMP4:
	case OutputMoQ:
		if c.MoQURL == "" {
			return errors.New("the moq output needs a relay URL")
		}
		if u, err := url.Parse(c.MoQURL); err != nil ||
			(!strings.EqualFold(u.Scheme, schemeMoQT) && !strings.EqualFold(u.Scheme, schemeHTTPS)) {
			return fmt.Errorf("the moq relay URL %q must use the %s:// or %s:// scheme", c.MoQURL, schemeMoQT,
				schemeHTTPS)
		}
		if c.MoQCatalog != CatalogMSF && c.MoQCatalog != CatalogHang {
			return fmt.Errorf("catalog format %q is not %s or %s", c.MoQCatalog, CatalogMSF, CatalogHang)
		}

	default:
		return fmt.Errorf("output %q is not %s, %s or %s", c.Output, OutputHTTP, OutputFMP4, OutputMoQ)
	}
	return nil
}

func (c *Config) validateLadder() error {
	// every rung is a valid frame size, each smaller than the one before, and the top rung is the stream's size
	ladder := c.Ladder()
	if len(ladder) > maxRungs {
		return fmt.Errorf("the ladder has %d renditions, more than %d", len(ladder), maxRungs)
	}
	for i, r := range ladder {
		switch {
		case r.Width < minDimension || r.Height < minDimension || r.Width > maxDimension || r.Height > maxDimension:
			return fmt.Errorf("rendition %s must be between %d and %d on each side", r, minDimension, maxDimension)
		case r.Width%2 != 0 || r.Height%2 != 0:
			return fmt.Errorf("rendition %s must be even on each side for 4:2:0 video", r)
		case r.Bitrate <= 0:
			return fmt.Errorf("rendition %s needs a positive bit rate", r)
		case i > 0 && (r.Width >= ladder[i-1].Width || r.Height >= ladder[i-1].Height):
			return fmt.Errorf("rendition %s must be smaller than %s, the rung above it", r, ladder[i-1])
		}
	}
	return nil
}

// FrameDuration is the length of one frame
func (c *Config) FrameDuration() time.Duration {
	if c.FPS <= 0 {
		return 0
	}
	return time.Second / time.Duration(c.FPS)
}

// FramesPerSegment is how many frames make one segment
func (c *Config) FramesPerSegment() int64 {
	return int64(c.Segment) * int64(c.FPS) / int64(time.Second)
}

// FramesPerChunk is how many frames make one CMAF chunk
func (c *Config) FramesPerChunk() int64 {
	if c.Chunk == 0 {
		return 1
	}
	return int64(c.Chunk) * int64(c.FPS) / int64(time.Second)
}
