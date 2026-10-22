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

// this many windows of segments are kept, so a client that read a manifest just before it moved can
// still fetch what it listed
const retainFactor = 2

// one track's media for one segment number: its chunks in order, which are written out one by one
type segment struct {
	number   int64
	start    time.Time
	chunks   []Chunk
	size     int
	duration time.Duration
	complete bool
}

// one encoded track, collected into numbered segments; segment numbers count segments from the Unix epoch
type track struct {
	kind      string
	rendition Rendition
	segDur    time.Duration
	start     time.Time
	startSeg  int64
	retain    int
	infoReady chan struct{}

	mu       sync.Mutex
	changed  chan struct{}
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
		changed: make(chan struct{}),
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
		t.cur = &segment{number: number, start: t.start.Add(time.Duration(number-t.startSeg) * t.segDur)}
		t.segs[number] = t.cur
	}
	t.cur.chunks = append(t.cur.chunks, c)
	t.cur.size += len(c.Data)
	t.notifyLocked()
	t.cur.duration += time.Duration(float64(c.Duration) / float64(t.info.Timescale) * float64(time.Second))
	if t.onChunk != nil {
		t.onChunk(t.cur, c)
	}
}

func (t *track) completeLocked() {
	// finishes the segment in progress and drops segments past the retention
	s := t.cur
	if s == nil {
		return
	}
	s.complete = true
	t.cur = nil
	t.newest = s.number
	t.notifyLocked()
	for t.newest-t.oldest >= int64(t.retain) {
		delete(t.segs, t.oldest)
		t.oldest++
	}
}

func (t *track) notifyLocked() {
	// wakes everyone waiting on the track: a chunk arrived, a segment completed, or the track finished; a part of
	// a segment in progress can be written out as it arrives from the same signal
	close(t.changed)
	t.changed = make(chan struct{})
}

func (t *track) finish() {
	// completes the last segment once the encoder has delivered everything
	t.mu.Lock()
	defer t.mu.Unlock()
	t.completeLocked()
	t.finished = true
	t.notifyLocked()
}

func (t *track) window(n int) []*segment {
	// returns up to n of the newest complete segments, oldest first
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

func (t *track) segment(ctx context.Context, number int64, wait time.Duration) *segment {
	// returns segment number, waiting up to wait for it to complete when it is the next one or two due
	t.mu.Lock()
	soon := number > t.newest && number <= t.newest+2 && !t.finished
	t.mu.Unlock()
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		t.mu.Lock()
		s, changed := t.segs[number], t.changed
		if s != nil && s.complete {
			t.mu.Unlock()
			return s
		}
		finished := t.finished
		t.mu.Unlock()
		if !soon || finished {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return nil
		}
	}
}
