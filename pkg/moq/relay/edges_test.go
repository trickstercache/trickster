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

package relay_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func TestCutThrough(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	outs := h.o.stream("video", 2)
	out := outs[0]
	if err := out.WriteObject(&message.SubgroupObject{ID: 0, PayloadLen: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	waitLargest(t, cl, "video", 2, 0)
	// a fetch reaching the object in progress waits for the rest of it
	freq, fr := fetch(t, cl, "video", fetchRange(2, 0, 2, 0))
	time.Sleep(20 * time.Millisecond)
	if _, err := out.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	if _, payloads := fetched(t, freq, fr); !slices.Equal(payloads, []string{"helloworld"}) {
		t.Errorf("fetched %v", payloads)
	}
	eventually(t, "the forwarded object", func() bool { return r.count() == 1 })
	if g := r.received(); g[0].payload != "helloworld" {
		t.Errorf("forwarded %+v", g)
	}
}

func TestUpstreamResetMidObject(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	out := h.o.stream("video", 2)[0]
	if err := writeObject(out, pubObj{id: 0, payload: []byte("whole")}); err != nil {
		t.Fatal(err)
	}
	if err := out.WriteObject(&message.SubgroupObject{ID: 1, PayloadLen: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("part")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the whole object", func() bool { return r.count() == 1 })
	out.Reset(message.ResetDeliveryTimeout)
	eventually(t, "the downstream reset", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.resets == 1
	})
	freq, fr := fetch(t, cl, "video", fetchRange(2, 0, 2, 1))
	fetched(t, freq, fr)
	fr.mu.Lock()
	defer fr.mu.Unlock()
	var kinds []message.RangeEnd
	for _, fo := range fr.fetched {
		kinds = append(kinds, fo.EndOfRange)
	}
	if len(fr.payloads) == 0 || fr.payloads[0] != "whole" {
		t.Errorf("fetched %v (%v)", fr.payloads, kinds)
	}
}

func TestOversizeObject(t *testing.T) {
	m := newMetrics()
	h := newHarness(t, relay.Options{MaxObjectSize: 4, Metrics: m}, nil)
	req, r := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	h.o.publish(t, "video", 0, objs("tiny", "far too large")...)
	eventually(t, "the small object", func() bool { return r.count() == 1 })
	eventually(t, "the drop", func() bool { return metricValue(m.DroppedObjects.WithLabelValues("oversize")) == 1 })
}

func TestMalformedEndOfGroup(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	h.o.publish(t, "video", 0, pubObj{id: 0, payload: []byte("a")}, pubObj{id: 1, status: message.StatusEndOfGroup})
	eventually(t, "the object", func() bool { return r.count() == 1 })
	h.o.publish(t, "video", 0, pubObj{id: 5, payload: []byte("past the end")})
	m := recv(t, r.messages, "PUBLISH_DONE")
	if done, ok := m.(*message.PublishDone); !ok || done.Code != message.DoneMalformedTrack {
		t.Fatalf("got %+v", m)
	}
}

func TestEndOfTrack(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	h.o.publish(t, "video", 0, pubObj{id: 0, payload: []byte("last")}, pubObj{id: 1, status: message.StatusEndOfTrack})
	eventually(t, "the object", func() bool { return r.count() == 1 })
	freq, fr := fetch(t, cl, "video", message.Params{})
	ok, payloads := fetched(t, freq, fr)
	if !ok.EndOfTrack || !slices.Equal(payloads, []string{"last"}) {
		t.Errorf("FETCH_OK %+v, fetched %v", ok, payloads)
	}
}

func TestGapFailures(t *testing.T) {
	h := newHarness(t, relay.Options{MaxGapFetches: 1}, nil)
	h.o.store("video", 0, objs("a")...)
	h.o.store("video", 2, objs("c")...)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 3)
	// group 1 does not exist upstream; groups 0-2 form one gap, fetched once
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 3, 2))
	_, payloads := fetched(t, freq, fr)
	if want := append([]string{"a", "c"}, groupPayloads(3)...); !slices.Equal(payloads, want) {
		t.Errorf("fetched %v, want %v", payloads, want)
	}
	h.up.down.Store(true)
	h.up.kill()
	freq, fr = fetch(t, cl, "video", fetchRange(0, 0, 0, 0))
	fetched(t, freq, fr)
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.fetched) != 1 || fr.fetched[0].EndOfRange != message.RangeEndUnknown {
		t.Errorf("an unreachable origin leaves the gap unknown: %+v", fr.fetched)
	}
}

func TestFetchUpdatesAndGoAway(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	if err := cl.GoAway("", 0); err != nil {
		t.Fatal(err)
	}
	req, _ := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	out := h.o.stream("video", 2)[0]
	if err := out.WriteObject(&message.SubgroupObject{ID: 0, PayloadLen: 4}); err != nil {
		t.Fatal(err)
	}
	waitLargest(t, cl, "video", 2, 0)
	// the fetch waits on the object in progress, so an update arrives while it runs
	freq, fr := fetch(t, cl, "video", fetchRange(2, 0, 2, 0))
	if _, err := freq.Response(context.Background()); err != nil {
		t.Fatal(err)
	}
	var p message.Params
	p.SetSubscriberPriority(1)
	if _, err := freq.Update(p); err != nil {
		t.Fatal(err)
	}
	if m := recv(t, fr.messages, "the update's answer"); m.Kind() != message.KindRequestOK {
		t.Errorf("got %+v", m)
	}
	if _, err := out.Write([]byte("done")); err != nil {
		t.Fatal(err)
	}
	recv(t, fr.fetchDone, "the fetch stream")
}

type failingCache struct {
	cache.Cache
}

func (failingCache) Store(string, []byte, time.Duration) error {
	return errors.New("disk full")
}

func TestSpillFailure(t *testing.T) {
	c, _ := newBoltCache(t)
	m := newMetrics()
	h := newHarness(t, relay.Options{LiveEdgeGroups: 1, Spill: true, Metrics: m}, failingCache{c})
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 0, 1)
	eventually(t, "the cache failure", func() bool { return metricValue(m.Errors.WithLabelValues("cache")) == 1 })
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 0, math.MaxUint64))
	if _, payloads := fetched(t, freq, fr); !slices.Equal(payloads, groupPayloads(0)) {
		t.Errorf("fetched %v", payloads)
	}
	if len(h.o.fetchAsks()) != 1 {
		t.Error("a group that failed to spill is fetched upstream")
	}
}

func TestUpdateBeforeSubscribeOK(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	h.o.hold = make(chan struct{})
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	eventually(t, "the upstream subscribe", func() bool { return h.o.subscribes.Load() == 1 })
	var p message.Params
	p.SetSubscriberPriority(3)
	if _, err := req.Update(p); err != nil {
		t.Fatal(err)
	}
	close(h.o.hold)
	subscribed(t, req)
	if m := recv(t, r.messages, "the update's answer"); m.Kind() != message.KindRequestOK {
		t.Errorf("a queued update is answered after SUBSCRIBE_OK: %+v", m)
	}
}

func TestFetchRangeFilters(t *testing.T) {
	h := newHarness(t, relay.Options{LiveEdgeGroups: 5}, nil)
	h.o.store("video", 0, objs(groupPayloads(0)...)...)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 1)
	p := fetchRange(0, 0, 1, 2)
	p.RangeFilters = []message.RangeFilter{{Type: message.ParamObjectIDFilter, Ranges: []wire.Range{{Start: 1, End: 1}}}}
	freq, fr := fetch(t, cl, "video", p)
	if _, payloads := fetched(t, freq, fr); !slices.Equal(payloads, []string{"g0-o1", "g1-o1"}) {
		t.Errorf("fetched %v", payloads)
	}
}

func TestDatagramFilters(t *testing.T) {
	h := newHarness(t, relay.Options{Prewarm: true}, nil)
	h.o.datagrams = true
	cl := h.client()
	var p message.Params
	p.RangeFilters = []message.RangeFilter{{Type: message.ParamObjectIDFilter, Ranges: []wire.Range{{Start: 1, End: 1}}}}
	freq, frec := subscribe(t, cl, "audio", p)
	subscribed(t, freq)
	var paused message.Params
	paused.SetForward(false)
	preq, prec := subscribe(t, cl, "audio", paused)
	subscribed(t, preq)
	h.o.publish(t, "audio", 0, objs("a", "b", "c")...)
	if d := recv(t, frec.datagrams, "the filtered datagram"); d.id != 1 {
		t.Errorf("got %+v", d)
	}
	time.Sleep(20 * time.Millisecond)
	if len(frec.datagrams) != 0 || len(prec.datagrams) != 0 {
		t.Error("filtered and paused subscribers receive nothing else")
	}
}

func TestLiveEdgeExpiry(t *testing.T) {
	h := newHarness(t, relay.Options{LiveEdgeGroups: 5}, nil)
	props, _ := wire.AppendKVs(nil, []wire.KV{{Type: message.PropMaxCacheDuration, Uint: 100}})
	h.o.props = props
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 0)
	time.Sleep(150 * time.Millisecond)
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 0, 2))
	if _, payloads := fetched(t, freq, fr); !slices.Equal(payloads, groupPayloads(0)) {
		t.Errorf("fetched %v", payloads)
	}
	if len(h.o.fetchAsks()) != 1 {
		t.Error("objects past MAX_CACHE_DURATION are fetched upstream again")
	}
}

func TestGapMarkersAndTimeouts(t *testing.T) {
	c, stores := newMemoryCache()
	h := newHarness(t, relay.Options{Spill: true}, c)
	h.o.markGaps = true
	h.o.store("video", 2, objs("c")...)
	cl := h.client()
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 2, 0))
	fetched(t, freq, fr)
	fr.mu.Lock()
	if len(fr.fetched) != 2 || fr.fetched[0].EndOfRange != message.RangeEndNonExistent || fr.payloads[1] != "c" {
		t.Errorf("an upstream marker is forwarded: %+v", fr.fetched)
	}
	fr.mu.Unlock()
	eventually(t, "the fetched group to spill", func() bool { return stores.Load() == 1 })

	h.o.fetchHold = make(chan struct{})
	defer close(h.o.fetchHold)
	h.o.store("video", 3, objs("d")...)
	p := fetchRange(3, 0, 3, 0)
	p.SetFillTimeout(30)
	freq, fr = fetch(t, cl, "video", p)
	fetched(t, freq, fr)
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.fetched) != 1 || fr.fetched[0].EndOfRange != message.RangeEndTimedOut {
		t.Errorf("a gap past its fill timeout is timed out: %+v", fr.fetched)
	}
}

func TestSpillAfterOpenRunEnds(t *testing.T) {
	c, stores := newMemoryCache()
	h := newHarness(t, relay.Options{LiveEdgeGroups: 1, Spill: true}, c)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	out := h.o.stream("video", 0)[0]
	if err := writeObject(out, pubObj{id: 0, payload: []byte("open")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the open group's object", func() bool { return r.count() == 1 })
	publishGroups(t, h, r, 1, 2)
	if stores.Load() != 1 {
		t.Errorf("only the closed group spills while the open one runs, got %d", stores.Load())
	}
	_ = out.Close()
	eventually(t, "the evicted group to spill when its stream ends", func() bool { return stores.Load() == 2 })
}

func TestCancelWhileObjectInProgress(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	req, _ := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	stay, srec := subscribe(t, cl, "video", message.Params{})
	subscribed(t, stay)
	out := h.o.stream("video", 0)[0]
	if err := out.WriteObject(&message.SubgroupObject{ID: 0, PayloadLen: 6}); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("par")); err != nil {
		t.Fatal(err)
	}
	waitLargest(t, cl, "video", 0, 0)
	req.Cancel(message.ResetCancelled)
	if _, err := out.Write([]byte("tly")); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	eventually(t, "the remaining subscriber's object", func() bool { return srec.count() == 1 })
}

func TestTrackStatusUpstreamDown(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	h.up.down.Store(true)
	req, err := h.client().Open(context.Background(), &message.TrackStatus{Track: track("video")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := req.Response(context.Background())
	if re, ok := m.(*message.RequestError); !ok || re.Code != message.RequestInternalError {
		t.Errorf("got %+v", m)
	}
}

func TestCanceledWhileUpstreamPending(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	h.o.hold = make(chan struct{})
	cl := h.client()
	req, _ := subscribe(t, cl, "video", message.Params{})
	eventually(t, "the upstream subscribe", func() bool { return h.o.subscribes.Load() == 1 })
	req.Cancel(message.ResetCancelled)
	time.Sleep(10 * time.Millisecond)
	close(h.o.hold)
	again, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, again)
	h.o.publish(t, "video", 0, objs("a")...)
	eventually(t, "objects for the later subscriber", func() bool { return r.count() == 1 })
}

func TestSpillWithoutCache(t *testing.T) {
	h := newHarness(t, relay.Options{LiveEdgeGroups: 1, Spill: true}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 0, 1, 2)
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 0, math.MaxUint64))
	if _, payloads := fetched(t, freq, fr); !slices.Equal(payloads, groupPayloads(0)) {
		t.Errorf("fetched %v", payloads)
	}
	if len(h.o.fetchAsks()) != 1 {
		t.Error("with no cache an evicted group is fetched upstream")
	}
}

func TestSessionCloseEndsSubscriptions(t *testing.T) {
	m := newMetrics()
	h := newHarness(t, relay.Options{Metrics: m, Linger: 10 * time.Millisecond}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	out := h.o.stream("video", 0)[0]
	if err := writeObject(out, pubObj{id: 0, payload: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the object", func() bool { return r.count() == 1 })
	_ = cl.Close(message.CodeNoError, "")
	eventually(t, "the subscription to end", func() bool {
		return metricValue(m.ActiveSubscriptions.WithLabelValues("downstream")) == 0
	})
	recv(t, h.o.ended, "the upstream subscription to end after the linger")
	_ = out.Close()
}
