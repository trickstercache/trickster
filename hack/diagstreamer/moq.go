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
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

const (
	moqUrgency        = 3
	moqRetryBackoff   = time.Second
	moqMaxBackoff     = 30 * time.Second
	reasonNotServed   = "not served by this publisher"
	reasonNoSuchTrack = "no such track"
	reasonNoObjects   = "no objects in range"
	reasonEnded       = "the stream ended"
)

type moqTrack struct {
	name    string
	media   *track
	catalog []byte
}

type moqPublisher struct {
	s        *Streamer
	ns       wire.Namespace
	tracks   map[string]*moqTrack
	catGroup uint64
}

func newMoQPublisher(s *Streamer, ns wire.Namespace) (*moqPublisher, error) {
	// both catalogs are served, so a reader of either finds the broadcast; they are fixed for the stream's life, in
	// a group numbered by its first segment, which a restarted publisher never reuses
	msf, err := msfCatalog(s)
	if err != nil {
		return nil, err
	}
	hang, err := hangCatalog(s)
	if err != nil {
		return nil, err
	}
	p := &moqPublisher{s: s, ns: ns, tracks: map[string]*moqTrack{}, catGroup: uint64(s.video.startSeg)} //nolint:gosec // epoch segment numbers
	p.tracks[msfCatalogTrack] = &moqTrack{name: msfCatalogTrack, catalog: msf}
	p.tracks[hangCatalogTrack] = &moqTrack{name: hangCatalogTrack, catalog: hang}
	for _, t := range s.tracks() {
		p.tracks[moqTrackName(t)] = &moqTrack{name: moqTrackName(t), media: t}
	}
	return p, nil
}

func moqTrackName(t *track) string {
	// a video track is named by its rendition, which readers pick it by
	if t.kind == KindVideo {
		return KindVideo + "-" + t.rendition.Name()
	}
	return t.kind
}

func moqObject(c Chunk) []byte {
	// an object is exactly one moof and its mdat (draft-lcurley-moq-hang), so a leading styp is left out
	b := c.Data
	for len(b) >= boxHeader {
		size := int(binary.BigEndian.Uint32(b))
		if string(b[4:8]) == boxMoof || size < boxHeader || size > len(b) {
			return b
		}
		b = b[size:]
	}
	return c.Data
}

func (p *moqPublisher) HandleGoAway(*session.Session, *message.GoAway) {}

func (p *moqPublisher) HandleRequest(r *session.Request) {
	sub := &moqSubscription{done: make(chan struct{})}
	r.SetHandlers(sub, nil)
	switch m := r.Message().(type) {
	case *message.Subscribe:
		p.subscribe(r, m, sub)
	case *message.Fetch:
		p.fetch(r, m)
	default:
		// a relay may ask about namespaces or tracks, and carries on without them when refused
		_ = r.Reject(message.RequestNotSupported, reasonNotServed, 0)
	}
}

func (p *moqPublisher) track(name message.FullTrackName) *moqTrack {
	if !name.Namespace.Equal(p.ns) {
		return nil
	}
	return p.tracks[string(name.Name)]
}

func (p *moqPublisher) subscribe(r *session.Request, m *message.Subscribe, sub *moqSubscription) {
	mt := p.track(m.Track)
	if mt == nil {
		_ = r.Reject(message.RequestDoesNotExist, reasonNoSuchTrack, 0)
		return
	}
	ok := &message.SubscribeOK{TrackAlias: r.Session().NewTrackAlias()}
	if mt.media != nil {
		// what streams next is the segment in progress from its start, so the Largest Object is the end of the
		// segment before it
		if l, has := p.lastComplete(mt); has {
			ok.Params.SetLargestObject(l)
		}
	}
	if r.Send(ok) != nil {
		return
	}
	if mt.media == nil {
		go p.sendCatalog(r, sub, mt.catalog)
		return
	}
	go p.stream(r, sub, mt)
}

func (p *moqPublisher) lastComplete(mt *moqTrack) (wire.Location, bool) {
	t := mt.media
	t.mu.Lock()
	seg := t.segs[t.newest]
	t.mu.Unlock()
	if seg == nil || !seg.complete {
		return wire.Location{}, false
	}
	if len(seg.chunks) == 0 {
		return wire.Location{}, false
	}
	return wire.Location{Group: uint64(seg.number), Object: uint64(len(seg.chunks) - 1)}, true //nolint:gosec // epoch segment numbers
}

func (p *moqPublisher) sendCatalog(r *session.Request, sub *moqSubscription, catalog []byte) {
	// each subscriber is sent the catalog at once, since it is read before anything else
	out, err := r.OpenSubgroup(r.Context(), message.SubgroupHeader{
		Group: p.catGroup, Priority: 0, EndOfGroup: true, FirstObject: true,
	}, 0, false)
	if err != nil {
		return
	}
	if writeObject(out, 0, catalog) != nil {
		return
	}
	if out.Close() == nil {
		sub.streams.Add(1)
	}
}

func writeObject(out *session.OutgoingSubgroup, id uint64, b []byte) error {
	if err := out.WriteObject(&message.SubgroupObject{ID: id, PayloadLen: uint64(len(b))}); err != nil {
		return err
	}
	_, err := out.Write(b)
	return err
}

func (p *moqPublisher) stream(r *session.Request, sub *moqSubscription, mt *moqTrack) {
	// one group per segment, opened at its keyframe, with each chunk's objects written as the chunk arrives
	t := mt.media
	ctx := r.Context()
	t.mu.Lock()
	n := t.newest + 1
	t.mu.Unlock()
	for {
		ok, err := p.streamGroup(ctx, r, sub, t, n)
		if err != nil || !ok {
			if err != nil && ctx.Err() == nil {
				slog.Warn("a MoQ group was not sent", "track", mt.name, "group", n, "error", err)
			}
			t.mu.Lock()
			ended := t.finished
			t.mu.Unlock()
			if ended && ctx.Err() == nil {
				// the stream ended: the subscription is done once its streams are counted
				if r.Send(&message.PublishDone{
					Code: message.DoneTrackEnded, StreamCount: sub.streams.Load(),
					Reason: reasonEnded,
				}) == nil {
					_ = r.CloseSend()
				}
			}
			return
		}
		n++
	}
}

func (p *moqPublisher) streamGroup(ctx context.Context, r *session.Request, sub *moqSubscription, t *track,
	n int64,
) (bool, error) {
	var out *session.OutgoingSubgroup
	sent, next := 0, uint64(0)
	var err error
	for {
		t.mu.Lock()
		seg, changed, finished := t.segs[n], t.changed, t.finished
		var chunks []Chunk
		complete := false
		if seg != nil {
			chunks, complete = seg.chunks[sent:], seg.complete
		}
		dropped := n < t.oldest
		t.mu.Unlock()
		if dropped || (seg == nil && finished) {
			return false, nil
		}
		for _, c := range chunks {
			if out == nil {
				out, err = r.OpenSubgroup(ctx, message.SubgroupHeader{
					Group: uint64(n), Priority: priority(t), EndOfGroup: true, FirstObject: next == 0, //nolint:gosec // epoch segment numbers
				}, moqUrgency, false)
				if err != nil {
					return false, err
				}
			}
			if err = writeObject(out, next, moqObject(c)); err != nil {
				return false, err
			}
			next++
		}
		sent += len(chunks)
		if complete {
			if out == nil {
				return true, nil
			}
			if err := out.Close(); err != nil {
				return false, err
			}
			sub.streams.Add(1)
			return true, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false, nil
		case <-sub.done:
			return false, nil
		}
	}
}

func priority(t *track) uint8 {
	// audio goes ahead of video when the connection is short of room
	if t.kind == KindAudio {
		return 1
	}
	return 2
}

func (p *moqPublisher) fetch(r *session.Request, m *message.Fetch) {
	// a FETCH is served from the segments still held: whole groups, each chunk's objects in order
	mt := p.track(m.Track)
	if mt == nil {
		_ = r.Reject(message.RequestDoesNotExist, reasonNoSuchTrack, 0)
		return
	}
	if m.Joining != nil {
		_ = r.Reject(message.RequestNotSupported, reasonNotServed, 0)
		return
	}
	type object struct {
		loc wire.Location
		b   []byte
	}
	var objs []object
	var largest wire.Location
	if mt.media == nil {
		largest = wire.Location{Group: p.catGroup}
		objs = append(objs, object{loc: largest, b: mt.catalog})
	} else {
		t := mt.media
		t.mu.Lock()
		var segs []*segment
		for n := t.oldest; n <= t.newest; n++ {
			if seg := t.segs[n]; seg != nil && seg.complete {
				segs = append(segs, seg)
			}
		}
		t.mu.Unlock()
		for _, seg := range segs {
			for i, c := range seg.chunks {
				loc := wire.Location{Group: uint64(seg.number), Object: uint64(i)} //nolint:gosec // epoch segment numbers
				objs = append(objs, object{loc: loc, b: moqObject(c)})
				largest = loc
			}
		}
	}
	if len(objs) == 0 {
		_ = r.Reject(message.RequestInvalidRange, reasonNoObjects, 0)
		return
	}
	start, end, _, ok := m.Params.LocationFilter.Bounds(&largest, true)
	if !ok {
		_ = r.Reject(message.RequestInvalidRange, reasonNoObjects, 0)
		return
	}
	if largest.Less(end) {
		end = largest
	}
	if r.Send(&message.FetchOK{EndLocation: end}) != nil {
		return
	}
	out, err := r.OpenFetch(r.Context(), r.RequestID(), message.GroupOrderAscending, moqUrgency)
	if err != nil {
		return
	}
	for _, o := range objs {
		if o.loc.Less(start) || end.Less(o.loc) {
			continue
		}
		fo := message.FetchObject{Group: o.loc.Group, Object: o.loc.Object, Priority: 1, PayloadLen: uint64(len(o.b))}
		if out.WriteObject(&fo) != nil {
			return
		}
		if _, err = out.Write(o.b); err != nil {
			return
		}
	}
	_ = out.Close()
	_ = r.CloseSend()
}

type moqSubscription struct {
	streams atomic.Uint64
	once    sync.Once
	done    chan struct{}
}

func (s *moqSubscription) HandleMessage(r *session.Request, m message.Message) {
	// a REQUEST_UPDATE, such as a renewal or a FORWARD change, is accepted; the stream always forwards
	if _, ok := m.(*message.RequestUpdate); ok {
		_ = r.Send(&message.RequestOK{})
	}
}

func (s *moqSubscription) HandleClose(*session.Request, error) {
	s.once.Do(func() { close(s.done) })
}

func moqNamespace(broadcast string) wire.Namespace {
	// a broadcast path's fields, split on '/', are its namespace, as moq-dev's tools name them
	var ns wire.Namespace
	for f := range strings.SplitSeq(broadcast, "/") {
		if f != "" {
			ns = append(ns, []byte(f))
		}
	}
	return ns
}

// Broadcast is the broadcast name the moq output publishes: the configured one, else the default with the catalog
// format's suffix, which moq-dev's readers pick a catalog by
func (c *Config) Broadcast() string {
	if c.MoQBroadcast != "" {
		return c.MoQBroadcast
	}
	return defaultBroadcast + "." + c.MoQCatalog
}

// StreamMoQ publishes the stream to the relay at cfg.MoQURL until ctx ends, dialing again when the session ends
func StreamMoQ(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	s, err := New(cfg)
	if err != nil {
		return err
	}
	errs := make(chan error, 1)
	go func() { errs <- s.Run(ctx) }()
	// an encoder that fails before its tracks are ready ends the output at once
	ready := make(chan bool, 1)
	go func() { ready <- s.ready(ctx) }()
	select {
	case err := <-errs:
		return err
	case ok := <-ready:
		if !ok {
			return errors.Join(errors.New("the encoder produced no tracks"), <-errs)
		}
	}
	p, err := newMoQPublisher(s, moqNamespace(cfg.Broadcast()))
	if err != nil {
		return errors.Join(err, <-errs)
	}
	backoff := moqRetryBackoff
	for ctx.Err() == nil {
		err := p.session(ctx, &s.cfg)
		if ctx.Err() != nil {
			break
		}
		slog.Warn("the MoQ session ended; dialing again", "error", err, "after", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
		}
		backoff = min(2*backoff, moqMaxBackoff)
	}
	return <-errs
}

func (p *moqPublisher) session(ctx context.Context, cfg *Config) error {
	// the publisher announces its namespace and serves the relay's SUBSCRIBEs and FETCHes until the session ends
	s, closeFn, err := dialMoQ(ctx, cfg, p)
	if err != nil {
		return err
	}
	defer closeFn()
	req, err := s.Open(ctx, &message.PublishNamespace{Prefix: p.ns}, &moqSubscription{done: make(chan struct{})}, nil)
	if err != nil {
		return err
	}
	resp, err := req.Response(ctx)
	if err != nil {
		return err
	}
	if re, refused := resp.(*message.RequestError); refused {
		return fmt.Errorf("the relay refused the namespace: %s (code %#x)", re.Reason, uint64(re.Code))
	}
	slog.Info("publishing the diagnostic stream", "relay", cfg.MoQURL, "broadcast", cfg.Broadcast(),
		"catalog", cfg.MoQCatalog, "draft", s.Codec().Draft())
	select {
	case <-ctx.Done():
		return nil
	case <-s.Done():
		return s.Err()
	}
}
