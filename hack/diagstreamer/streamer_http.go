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
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// Streamer runs one render and encode pipeline and serves what it produces
type Streamer struct {
	cfg      Config
	p        *producer
	video    *track
	videos   []*track
	audio    *track
	streamID string
	textInit []byte
	ctx      context.Context
}

// New prepares a stream; Run produces it
func New(cfg Config) (*Streamer, error) {
	return newStreamer(cfg, time.Now)
}

func newStreamer(cfg Config, now func() time.Time) (*Streamer, error) {
	cfg = cfg.normalized()
	ladder := cfg.Ladder()
	p, err := newProducer(cfg, ladder, now)
	if err != nil {
		return nil, err
	}
	startSeg := p.tl.segment(p.start)
	s := &Streamer{
		cfg: cfg, p: p, streamID: "s" + strconv.FormatInt(startSeg, 10), ctx: context.Background(),
		textInit: wvttInit(),
	}
	for _, rn := range ladder {
		t := newTrack(KindVideo, &cfg, p.startTime(), startSeg)
		t.rendition = rn
		s.videos = append(s.videos, t)
	}
	// the top rendition's track sets the timeline the captions and subtitles follow
	s.video = s.videos[0]
	if cfg.Audio {
		s.audio = newTrack(KindAudio, &cfg, p.startTime(), startSeg)
	}
	return s, nil
}

// Run renders and encodes until ctx ends
func (s *Streamer) Run(ctx context.Context) error {
	s.ctx = ctx
	sinks := make([]TrackSink, len(s.videos))
	for i, t := range s.videos {
		sinks[i] = t
	}
	enc, err := NewTrackEncoder(context.WithoutCancel(ctx), s.cfg, sinks, s.audio)
	if err != nil {
		return err
	}
	err = errors.Join(s.p.run(ctx, enc, 0), enc.Close())
	for _, t := range s.videos {
		t.finish()
	}
	if s.audio != nil {
		s.audio.finish()
	}
	return err
}

func (s *Streamer) tracks() []*track {
	// lists the media tracks in manifest order
	if s.audio == nil {
		return s.videos
	}
	return append(slices.Clip(s.videos), s.audio)
}

func (s *Streamer) rendition(name string) *track {
	for _, t := range s.videos {
		if t.rendition.Name() == name {
			return t
		}
	}
	return nil
}

func (s *Streamer) ready(ctx context.Context) bool {
	// waits until every track has its initialization segment
	wait := time.NewTimer(2*s.cfg.Segment + 2*time.Second)
	defer wait.Stop()
	for _, t := range s.tracks() {
		select {
		case <-t.infoReady:
		case <-ctx.Done():
			return false
		case <-wait.C:
			return false
		}
	}
	return true
}

func (s *Streamer) codecs(video *track) string {
	out := video.info.Codec
	if s.audio != nil {
		out += "," + s.audio.info.Codec
	}
	return out
}

func (s *Streamer) bandwidth(video *track) int {
	// the encoder's cap, the audio's, and a tenth for the containers
	bw := video.rendition.Bitrate
	if s.cfg.Audio {
		bw += s.cfg.AudioBitrate
	}
	return bw + bw/10
}

func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}

func isoDuration(d time.Duration) string {
	return fmt.Sprintf("PT%sS", strconv.FormatFloat(d.Seconds(), 'f', -1, 64))
}
