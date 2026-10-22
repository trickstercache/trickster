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

// Command diagstreamer generates a live diagnostic test stream: a rendered picture with a clock, a moving logo and
// a timing strip, and a beeping tone, served as live HLS and DASH, written as fragmented MP4, or published to MoQ.
package main

import (
	"context"
	"flag"
	"fmt"
	"image/png"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	ds "github.com/trickstercache/trickster/v2/hack/diagstreamer"
)

type fieldList struct {
	fields *[]ds.Field
	set    bool
}

func (f *fieldList) String() string {
	if f.fields == nil {
		return ""
	}
	parts := make([]string, 0, len(*f.fields))
	for _, fl := range *f.fields {
		parts = append(parts, fl.String())
	}
	return strings.Join(parts, ",")
}

func (f *fieldList) Set(v string) error {
	fl, err := ds.ParseField(v)
	if err != nil {
		return err
	}
	if !f.set {
		// a field given on the command line replaces those from the environment
		*f.fields, f.set = nil, true
	}
	*f.fields = append(*f.fields, fl)
	return nil
}

type ladder struct {
	r *[]ds.Rendition
}

func (l ladder) String() string {
	if l.r == nil {
		return ""
	}
	parts := make([]string, 0, len(*l.r))
	for _, r := range *l.r {
		parts = append(parts, r.String())
	}
	return strings.Join(parts, ",")
}

func (l ladder) Set(s string) (err error) {
	*l.r, err = ds.ParseRenditions(s)
	return err
}

type bitrate struct {
	v *int
}

func (b bitrate) String() string {
	if b.v == nil {
		return ""
	}
	return strconv.Itoa(*b.v)
}

func (b bitrate) Set(s string) (err error) {
	*b.v, err = ds.ParseBitrate(s)
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "diagstreamer: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := ds.ConfigFromEnv(ds.DefaultConfig())
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("diagstreamer", flag.ContinueOnError)
	fs.IntVar(&cfg.Width, "width", cfg.Width, "video width ("+ds.EnvWidth+")")
	fs.IntVar(&cfg.Height, "height", cfg.Height, "video height ("+ds.EnvHeight+")")
	fs.IntVar(&cfg.FPS, "fps", cfg.FPS, "whole-number frame rate ("+ds.EnvFPS+")")
	fs.DurationVar(&cfg.Segment, "segment", cfg.Segment, "segment and GOP length ("+ds.EnvSegment+")")
	fs.DurationVar(&cfg.Chunk, "chunk", cfg.Chunk, "CMAF chunk length, 0 for one frame ("+ds.EnvChunk+")")
	fs.Var(bitrate{&cfg.VideoBitrate}, "video-bitrate", "video bit rate, such as 3M ("+ds.EnvVideoBitrate+")")
	fs.BoolVar(&cfg.Audio, "audio", cfg.Audio, "add the beeping tone track ("+ds.EnvAudio+")")
	fs.Var(bitrate{&cfg.AudioBitrate}, "audio-bitrate", "audio bit rate, such as 128k ("+ds.EnvAudioBitrate+")")
	fs.StringVar(&cfg.Title, "title", cfg.Title, "title shown in the picture ("+ds.EnvTitle+")")
	fs.Var(&fieldList{fields: &cfg.Fields}, "field", "key=value shown in the picture, repeatable ("+ds.EnvFields+
		" takes k=v,k=v)")
	fs.BoolVar(&cfg.Captions, "captions", cfg.Captions, "add caption tracks and a burned-in caption ("+ds.EnvCaptions+")")
	fs.DurationVar(&cfg.ColorPeriod, "color-period", cfg.ColorPeriod, "background color period, 0 to keep one ("+
		ds.EnvColorPeriod+")")
	fs.IntVar(&cfg.Window, "window", cfg.Window, "segments listed in a live manifest ("+ds.EnvWindow+")")
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "address the http output serves on ("+ds.EnvListen+")")
	fs.StringVar(&cfg.Output, "out", cfg.Output, "http, fmp4 (to stdout) or moq ("+ds.EnvOutput+")")
	fs.StringVar(&cfg.MoQURL, "moq-url", cfg.MoQURL, "relay the moq output publishes to, moqt:// or https:// ("+
		ds.EnvMoQURL+")")
	fs.StringVar(&cfg.MoQCatalog, "moq-catalog", cfg.MoQCatalog, "catalog format of the moq output: "+ds.CatalogMSF+
		" or "+ds.CatalogHang+" ("+ds.EnvMoQCatalog+")")
	fs.StringVar(&cfg.MoQBroadcast, "moq-broadcast", cfg.MoQBroadcast, "broadcast name of the moq output ("+
		ds.EnvMoQBroadcast+")")
	fs.BoolVar(&cfg.MoQInsecure, "moq-insecure", cfg.MoQInsecure, "skip verifying the relay's certificate ("+
		ds.EnvMoQInsecure+")")
	fs.StringVar(&cfg.MoQCA, "moq-ca", cfg.MoQCA, "PEM bundle trusted for the relay's certificate ("+ds.EnvMoQCA+")")
	fs.StringVar(&cfg.FFmpeg, "ffmpeg", cfg.FFmpeg, "path of the ffmpeg binary ("+ds.EnvFFmpeg+")")
	fs.Var(ladder{&cfg.Renditions}, "renditions", "the whole video ladder, largest first, such as "+
		"1920x1080@6M,1280x720@3M; unset, -width, -height and -video-bitrate are the top rung over the default "+
		"720p, 480p and 360p rungs below it ("+ds.EnvRenditions+")")
	snapshot := fs.String("snapshot", "", "write the current frame to this PNG file and exit")
	if err = fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	if *snapshot != "" {
		return writeSnapshot(cfg, *snapshot)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return ds.Run(ctx, cfg)
}

func writeSnapshot(cfg ds.Config, path string) error {
	r, err := ds.NewRenderer(cfg)
	if err != nil {
		return err
	}
	r.Render(r.FrameAt(time.Now()))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err = png.Encode(f, r.RGBA()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
