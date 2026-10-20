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
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	lagReport         = 2 * time.Second
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// producer renders frames and tone samples in step with the wall clock and feeds them to an encoder
type producer struct {
	cfg   Config
	tl    timeline
	r     *Renderer
	tone  Tone
	start int64
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func newProducer(cfg Config, now func() time.Time) (*producer, error) {
	r, err := NewRenderer(cfg)
	if err != nil {
		return nil, err
	}
	p := &producer{cfg: cfg, tl: newTimeline(&cfg), r: r, now: now, sleep: sleepContext}
	// encoding starts on a segment boundary, so the first keyframe begins a whole segment
	p.start = (p.tl.frameAt(p.now())/p.tl.perSegment + 1) * p.tl.perSegment
	return p, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// startTime is when the first encoded frame is shown
func (p *producer) startTime() time.Time {
	return p.tl.at(p.start)
}

// run feeds frames from p.start until ctx ends or frames frames have been written, when frames is positive
func (p *producer) run(ctx context.Context, enc Encoder, frames int64) error {
	lagged := false
	for n := p.start; frames <= 0 || n < p.start+frames; n++ {
		at := p.tl.at(n)
		if err := p.sleep(ctx, at.Sub(p.now())); err != nil {
			return nil
		}
		if lag := p.now().Sub(at); lag > lagReport && !lagged {
			slog.Warn("rendering is behind the clock; the stream will lag", "behind", lag.Round(time.Millisecond))
			lagged = true
		}
		if err := enc.WriteFrame(p.r.Render(n)); err != nil {
			return err
		}
		if p.cfg.Audio {
			from := p.tl.sampleAt(n)
			if err := enc.WriteAudio(p.tone.Samples(from, int(p.tl.sampleAt(n+1)-from))); err != nil {
				return err
			}
		}
	}
	return nil
}

// StreamEncoder writes the stream as one muxed fragmented MP4 to w until ctx ends
func StreamEncoder(ctx context.Context, cfg Config, w io.Writer) error {
	return streamEncoder(ctx, cfg, w, 0)
}

func streamEncoder(ctx context.Context, cfg Config, w io.Writer, frames int64) error {
	p, err := newProducer(cfg, time.Now)
	if err != nil {
		return err
	}
	// the encoder outlives ctx so it can finish the fragment in progress after the last frame
	enc, err := NewMuxedEncoder(context.WithoutCancel(ctx), cfg, w)
	if err != nil {
		return err
	}
	return errors.Join(p.run(ctx, enc, frames), enc.Close())
}

// StreamHLS starts the stream and returns its handler, which serves HLS under /hls/; the stream runs until ctx ends
func StreamHLS(ctx context.Context, cfg Config) (http.Handler, error) {
	return startStreamer(ctx, cfg)
}

// StreamDASH starts the stream and returns its handler, which serves DASH under /dash/; the stream runs until ctx ends
func StreamDASH(ctx context.Context, cfg Config) (http.Handler, error) {
	return startStreamer(ctx, cfg)
}

func startStreamer(ctx context.Context, cfg Config) (http.Handler, error) {
	s, err := New(cfg)
	if err != nil {
		return nil, err
	}
	go func() {
		if err := s.Run(ctx); err != nil {
			slog.Error("the stream stopped", "error", err)
		}
	}()
	return s.Handler(), nil
}

// Run generates the stream cfg describes and delivers it to cfg.Output until ctx ends
func Run(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	switch cfg.Output {
	case OutputFMP4:
		return StreamEncoder(ctx, cfg, os.Stdout)
	case OutputHTTP:
		return serveHTTP(ctx, cfg)
	}
	return errors.New("output " + cfg.Output + " is not built yet")
}

func serveHTTP(ctx context.Context, cfg Config) error {
	s, err := New(cfg)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: readHeaderTimeout}
	slog.Info("serving the diagnostic stream", "page", "http://"+ln.Addr().String()+"/", "hls", "/hls/master.m3u8",
		"dash", "/dash/manifest.mpd", "starts", s.p.startTime().Format(time.RFC3339Nano))
	errs := make(chan error, 2)
	go func() { errs <- s.Run(ctx) }()
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	select {
	case err = <-errs:
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	return errors.Join(err, srv.Shutdown(sctx))
}
