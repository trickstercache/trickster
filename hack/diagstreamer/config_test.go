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
	"strings"
	"testing"
	"time"
)

func TestDefaultConfigValidates(t *testing.T) {
	c := DefaultConfig()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.FramesPerSegment() != 60 || c.FramesPerChunk() != 1 {
		t.Errorf("frames per segment %d, per chunk %d", c.FramesPerSegment(), c.FramesPerChunk())
	}
	c.Chunk = 500 * time.Millisecond
	if c.FramesPerChunk() != 15 {
		t.Errorf("a half-second chunk at 30fps is 15 frames, got %d", c.FramesPerChunk())
	}
	var zero Config
	if zero.FrameDuration() != 0 {
		t.Error("no frame rate, no frame duration")
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{
		EnvWidth: "640", EnvHeight: "360", EnvFPS: "25", EnvSegment: "4s", EnvChunk: "200ms", EnvVideoBitrate: "1.5M",
		EnvAudio: "false", EnvAudioBitrate: "96k", EnvTitle: "Lab", EnvFields: "Site=lab, Encoder = x264 ,",
		EnvCaptions: "false", EnvColorPeriod: "0s", EnvWindow: "5", EnvListen: ":9000",
		EnvOutput: OutputMoQ, EnvMoQURL: "moqt://relay.example.com/", EnvFFmpeg: "/usr/bin/ffmpeg",
	}
	c, err := configFrom(DefaultConfig(), func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if c.Width != 640 || c.Height != 360 || c.FPS != 25 || c.Segment != 4*time.Second || c.Chunk != 200*time.Millisecond ||
		c.VideoBitrate != 1_500_000 || c.Audio || c.AudioBitrate != 96_000 || c.Title != "Lab" || c.Captions ||
		c.ColorPeriod != 0 || c.Window != 5 || c.Listen != ":9000" || c.Output != OutputMoQ ||
		c.MoQURL == "" || c.FFmpeg != "/usr/bin/ffmpeg" {
		t.Errorf("config %+v", c)
	}
	if len(c.Fields) != 2 || c.Fields[1] != (Field{"Encoder", "x264"}) || c.Fields[0].String() != "Site=lab" {
		t.Errorf("fields %+v", c.Fields)
	}
	if err = c.Validate(); err != nil {
		t.Error(err)
	}
	bad := map[string]string{EnvWidth: "wide", EnvAudio: "maybe", EnvSegment: "2", EnvVideoBitrate: "-1",
		EnvFields: "novalue"}
	_, err = configFrom(DefaultConfig(), func(k string) (string, bool) { v, ok := bad[k]; return v, ok })
	for _, name := range []string{EnvWidth, EnvAudio, EnvSegment, EnvVideoBitrate, EnvFields} {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s is reported: %v", name, err)
		}
	}
	if _, err = ConfigFromEnv(DefaultConfig()); err != nil {
		t.Error(err)
	}
}

func TestParseBitrate(t *testing.T) {
	for in, want := range map[string]int{"3M": 3_000_000, "128k": 128_000, "2.5m": 2_500_000, "64K": 64_000,
		"800000": 800_000} {
		if got, err := ParseBitrate(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "k", "fast", "0", "-3M"} {
		if _, err := ParseBitrate(in); err == nil {
			t.Errorf("%q is refused", in)
		}
	}
}

func TestValidate(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"too small":           func(c *Config) { c.Width = 64 },
		"odd":                 func(c *Config) { c.Height = 721 },
		"no frame rate":       func(c *Config) { c.FPS = 0 },
		"short segment":       func(c *Config) { c.Segment = 500 * time.Millisecond },
		"partial frames":      func(c *Config) { c.Captions = false; c.Segment = 1010 * time.Millisecond },
		"chunk not dividing":  func(c *Config) { c.Chunk = 700 * time.Millisecond },
		"no bit rate":         func(c *Config) { c.VideoBitrate = 0 },
		"negative period":     func(c *Config) { c.ColorPeriod = -1 },
		"short window":        func(c *Config) { c.Window = 2 },
		"fractional captions": func(c *Config) { c.Segment = 1500 * time.Millisecond },
		"unknown output":      func(c *Config) { c.Output = "rtmp" },
		"moq without a relay": func(c *Config) { c.Output = OutputMoQ },
		"moq relay scheme":    func(c *Config) { c.Output, c.MoQURL = OutputMoQ, "ftp://relay.example.com/" },
	} {
		c := DefaultConfig()
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s is refused", name)
		}
	}
}

func TestLadder(t *testing.T) {
	c := DefaultConfig()
	got := c.Ladder()
	if len(got) != 4 || got[0] != (Rendition{1920, 1080, 6_000_000}) || got[3].Name() != "360p" {
		t.Fatalf("the default ladder is 1080p, 720p, 480p and 360p: %v", got)
	}
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
	// a smaller top rung keeps the default rungs below it
	c.Width, c.Height, c.VideoBitrate = 1280, 720, 3_000_000
	if got = c.Ladder(); len(got) != 3 || got[1].Name() != "480p" {
		t.Errorf("a 720p top rung: %v", got)
	}
	// a given ladder is the whole ladder, and names the top rung
	r, err := ParseRenditions(" 1280x720@3M, 640x360@800k ,")
	if err != nil {
		t.Fatal(err)
	}
	c.Renditions = r
	if n := c.normalized(); n.Width != 1280 || n.Height != 720 || n.VideoBitrate != 3_000_000 || len(n.Ladder()) != 2 {
		t.Errorf("normalized %+v", n)
	}
	if r[1].String() != "640x360@800000" {
		t.Errorf("string %s", r[1])
	}
	for _, in := range []string{"1280x720", "1280@3M", "wx720@3M", "1280xh@3M", "1280x720@fast"} {
		if _, err := ParseRenditions(in); err == nil {
			t.Errorf("%q is refused", in)
		}
	}
	for name, ladder := range map[string]string{
		"growing":  "640x360@1M,1280x720@3M",
		"odd":      "1280x720@3M,641x360@1M",
		"tiny":     "1280x720@3M,64x36@1M",
		"same":     "1280x720@3M,1280x720@1M",
		"too many": "1920x1080@1M,1800x1000@1M,1700x900@1M,1600x800@1M,1500x700@1M,1400x600@1M,1300x500@1M,1200x400@1M,1100x300@1M",
	} {
		c := DefaultConfig()
		c.Renditions, _ = ParseRenditions(ladder)
		if c.Validate() == nil {
			t.Errorf("a %s ladder is refused", name)
		}
	}
	c = DefaultConfig()
	c.Renditions = []Rendition{{Width: 640, Height: 360}}
	if c.Validate() == nil {
		t.Error("a rendition without a bit rate is refused")
	}
	env := map[string]string{EnvRenditions: "854x480@1.5M"}
	c, err = configFrom(DefaultConfig(), func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil || len(c.Renditions) != 1 || c.Renditions[0].Height != 480 {
		t.Errorf("environment ladder %v %v", c.Renditions, err)
	}
	env[EnvRenditions] = "bad"
	if _, err = configFrom(DefaultConfig(), func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err == nil {
		t.Error("a bad environment ladder is refused")
	}
}
