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
	"log/slog"
	"sync"
	"time"
)

// retainFactor keeps this many windows of segments, so a client that read a manifest just before it moved can
// still fetch what it listed
const retainFactor = 2

// segment is one track's media for one segment number: the chunks in order, and their bytes once complete
type segment struct {
	number   int64
	start    time.Time
	chunks   []Chunk
	data     []byte
	duration time.Duration
	complete bool
	ready    chan struct{}
}

// track collects one encoded track into numbered segments; segment numbers count segments from the Unix epoch
type track struct {
	kind      string
	segDur    time.Duration
	start     time.Time
	startSeg  int64
	retain    int
	infoReady chan struct{}

	mu       sync.Mutex
	info     TrackInfo
	segs     map[int64]*segment
	cur      *segment
	newest   int64
	oldest   int64
	onChunk  func(*segment, Chunk)
	finished bool
}

func newTrack(kind string, cfg *Config, start time.Time, startSeg int64) *track {
	return &track{
		kind: kind, segDur: cfg.Segment, start: start, startSeg: startSeg, retain: cfg.Window * retainFactor,
		infoReady: make(chan struct{}), segs: make(map[int64]*segment), newest: startSeg - 1, oldest: startSeg,
	}
}

func (t *track) Init(info TrackInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.info.Init != nil {
		return
	}
	t.info = info
	close(t.infoReady)
}

func (t *track) Chunk(c Chunk) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.info.Timescale == 0 {
		return
	}
	offset := time.Duration(float64(c.DecodeTime) / float64(t.info.Timescale) * float64(time.Second))
	number := t.startSeg + int64(offset/t.segDur)
	if t.cur == nil || number > t.cur.number {
		t.completeLocked()
		if t.kind == KindVideo && !c.Key {
			slog.Warn("a video segment does not begin with a keyframe", "segment", number)
		}
		t.cur = &segment{
			number: number, start: t.start.Add(time.Duration(number-t.startSeg) * t.segDur),
			ready: make(chan struct{}),
		}
		t.segs[number] = t.cur
	}
	t.cur.chunks = append(t.cur.chunks, c)
	t.cur.duration += time.Duration(float64(c.Duration) / float64(t.info.Timescale) * float64(time.Second))
	if t.onChunk != nil {
		t.onChunk(t.cur, c)
	}
}

// completeLocked finishes the segment in progress and drops segments past the retention
func (t *track) completeLocked() {
	s := t.cur
	if s == nil {
		return
	}
	size := 0
	for _, c := range s.chunks {
		size += len(c.Data)
	}
	s.data = make([]byte, 0, size)
	for _, c := range s.chunks {
		s.data = append(s.data, c.Data...)
	}
	s.complete = true
	close(s.ready)
	t.cur = nil
	t.newest = s.number
	for t.newest-t.oldest >= int64(t.retain) {
		delete(t.segs, t.oldest)
		t.oldest++
	}
}

// finish completes the last segment once the encoder has delivered everything
func (t *track) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.completeLocked()
	t.finished = true
}

// window returns up to n of the newest complete segments, oldest first
func (t *track) window(n int) []*segment {
	t.mu.Lock()
	defer t.mu.Unlock()
	from := max(t.newest-int64(n)+1, t.oldest)
	out := make([]*segment, 0, n)
	for i := from; i <= t.newest; i++ {
		if s := t.segs[i]; s != nil && s.complete {
			out = append(out, s)
		}
	}
	return out
}

// segment returns segment number, waiting up to wait for it to complete when it is the next one or two due
func (t *track) segment(ctx context.Context, number int64, wait time.Duration) *segment {
	t.mu.Lock()
	s := t.segs[number]
	done := s != nil && s.complete
	soon := number > t.newest && number <= t.newest+2 && !t.finished
	t.mu.Unlock()
	if done {
		return s
	}
	if !soon {
		return nil
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		if s != nil {
			select {
			case <-s.ready:
				return s
			case <-ctx.Done():
				return nil
			case <-deadline.C:
				return nil
			}
		}
		// the segment has not begun; look again once a chunk might have started it
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return nil
		case <-time.After(t.segDur / 20):
		}
		t.mu.Lock()
		s = t.segs[number]
		t.mu.Unlock()
	}
}
