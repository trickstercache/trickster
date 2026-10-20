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
	"context"
	"image"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func needFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath(defaultFFmpeg)
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	return path
}

func TestMuxedEncoderEndToEnd(t *testing.T) {
	ffmpegPath := needFFmpeg(t)
	t.Parallel()
	cfg := testConfig()
	cfg.FFmpeg = ffmpegPath
	var out bytes.Buffer
	if err := streamEncoder(context.Background(), cfg, &out, 15); err != nil {
		t.Fatal(err)
	}
	var types []string
	traks := 0
	if err := children(out.Bytes(), func(typ string, b []byte) error {
		types = append(types, typ)
		if typ == boxMoov {
			_ = children(b[boxHeader:], func(child string, _ []byte) error {
				if child == boxTrak {
					traks++
				}
				return nil
			})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(types) < 4 || types[0] != boxFtyp || types[1] != boxMoov || traks != 2 || !strings.Contains(strings.Join(types, ","), "moof,mdat") {
		t.Fatalf("boxes %v with %d traks", types, traks)
	}
	// the first decoded frame's timing strip survives encoding and names a segment boundary
	cmd := exec.Command(ffmpegPath, "-v", "error", "-i", "pipe:0", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt",
		"yuv420p", "pipe:1")
	cmd.Stdin = &out
	raw, err := cmd.Output()
	if err != nil || len(raw) != cfg.Width*cfg.Height*3/2 {
		t.Fatalf("decoding %d bytes: %v", len(raw), err)
	}
	ys, cs := cfg.Width*cfg.Height, cfg.Width*cfg.Height/4
	img := &image.YCbCr{Y: raw[:ys], Cb: raw[ys : ys+cs], Cr: raw[ys+cs:], YStride: cfg.Width, CStride: cfg.Width / 2,
		SubsampleRatio: image.YCbCrSubsampleRatio420, Rect: image.Rect(0, 0, cfg.Width, cfg.Height)}
	got, ok := DecodeTiming(img)
	if !ok || got.At.UnixMilli()%cfg.Segment.Milliseconds() != 0 ||
		got.Frame != uint32(got.At.UnixMilli()*int64(cfg.FPS)/1000) || time.Since(got.At) > time.Minute {
		t.Errorf("decoded %+v %v", got, ok)
	}
}

func TestTrackEncoderEndToEnd(t *testing.T) {
	ffmpegPath := needFFmpeg(t)
	t.Parallel()
	cfg := testConfig()
	cfg.FFmpeg = ffmpegPath
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	wait, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	seg := s.video.segment(wait, s.video.startSeg, 10*time.Second)
	aseg := s.audio.segment(wait, s.audio.startSeg, 10*time.Second)
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if seg == nil || aseg == nil {
		t.Fatal("the first segments complete")
	}
	if !strings.HasPrefix(s.video.info.Codec, codecAVC1+".") || s.audio.info.Codec != codecAAC ||
		s.video.info.Timescale != videoTimescale90k || s.audio.info.Timescale != SampleRate {
		t.Errorf("tracks %+v %+v", s.video.info.Codec, s.audio.info.Codec)
	}
	if len(seg.chunks) != int(cfg.FramesPerSegment()) || !seg.chunks[0].Key || seg.chunks[1].Key ||
		seg.duration != cfg.Segment {
		t.Errorf("video segment: %d chunks over %s", len(seg.chunks), seg.duration)
	}
	if aseg.duration < cfg.Segment-50*time.Millisecond || aseg.duration > cfg.Segment+50*time.Millisecond {
		t.Errorf("audio segment lasts %s", aseg.duration)
	}
}

func TestEncoderFailure(t *testing.T) {
	ffmpegPath := needFFmpeg(t)
	cfg := testConfig()
	cfg.FFmpeg = ffmpegPath
	cfg.VideoBitrate = -1 // ffmpeg refuses a negative bit rate
	var r recorder
	enc, err := NewTrackEncoder(context.Background(), cfg, &r, &r)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, cfg.Width*cfg.Height*3/2)
	for range 100 {
		if err = enc.WriteFrame(frame); err != nil {
			break
		}
	}
	if err = enc.Close(); err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Errorf("ffmpeg's failure is reported: %v", err)
	}
}
