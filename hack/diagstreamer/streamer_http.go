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
	"strconv"
	"time"
)

// Streamer runs one render and encode pipeline and serves what it produces
type Streamer struct {
	cfg      Config
	p        *producer
	video    *track
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
	p, err := newProducer(cfg, now)
	if err != nil {
		return nil, err
	}
	startSeg := p.tl.segment(p.start)
	s := &Streamer{
		cfg: cfg, p: p, streamID: "s" + strconv.FormatInt(startSeg, 10), ctx: context.Background(),
		textInit: wvttInit(),
	}
	s.video = newTrack(KindVideo, &cfg, p.startTime(), startSeg)
	if cfg.Audio {
		s.audio = newTrack(KindAudio, &cfg, p.startTime(), startSeg)
	}
	return s, nil
}

// Run renders and encodes until ctx ends
func (s *Streamer) Run(ctx context.Context) error {
	s.ctx = ctx
	enc, err := NewTrackEncoder(context.WithoutCancel(ctx), s.cfg, s.video, s.audio)
	if err != nil {
		return err
	}
	err = errors.Join(s.p.run(ctx, enc, 0), enc.Close())
	s.video.finish()
	if s.audio != nil {
		s.audio.finish()
	}
	return err
}

// tracks lists the media tracks in manifest order
func (s *Streamer) tracks() []*track {
	if s.audio == nil {
		return []*track{s.video}
	}
	return []*track{s.video, s.audio}
}

// ready waits until every track has its initialization segment
func (s *Streamer) ready(ctx context.Context) bool {
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

func (s *Streamer) codecs() string {
	out := s.video.info.Codec
	if s.audio != nil {
		out += "," + s.audio.info.Codec
	}
	return out
}

func (s *Streamer) bandwidth() int {
	bw := s.cfg.VideoBitrate
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
