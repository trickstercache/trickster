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
	"fmt"
	"math"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func groupPayloads(groups ...uint64) []string {
	var out []string
	for _, g := range groups {
		for i := range 3 {
			out = append(out, fmt.Sprintf("g%d-o%d", g, i))
		}
	}
	return out
}

func publishGroups(t *testing.T, h *harness, r *rec, groups ...uint64) {
	t.Helper()
	// one group at a time, so the relay sees them in order and evicts and spills them in order
	for _, g := range groups {
		before := r.count()
		h.o.publish(t, "video", g, objs(groupPayloads(g)...)...)
		eventually(t, "the published objects", func() bool { return r.count() == before+3 })
	}
}

func TestFetchFromLiveEdge(t *testing.T) {
	h := newHarness(t, relay.Options{LiveEdgeGroups: 5}, nil)
	c := h.client()
	req, r := subscribe(t, c, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 0, 1, 2)
	freq, fr := fetch(t, c, "video", fetchRange(0, 1, 2, math.MaxUint64))
	ok, payloads := fetched(t, freq, fr)
	if ok.EndLocation != (wire.Location{Group: 2, Object: 2}) {
		t.Errorf("FETCH_OK ends at Largest Object, got %+v", ok.EndLocation)
	}
	if want := groupPayloads(0, 1, 2)[1:]; !slices.Equal(payloads, want) {
		t.Errorf("fetched %v, want %v", payloads, want)
	}
	if asks := h.o.fetchAsks(); len(asks) != 0 {
		t.Errorf("the live edge serves without upstream fetches, got %+v", asks)
	}
	// a range closing at an ended group's end names its last object rather than an open end
	freq, fr = fetch(t, c, "video", fetchRange(0, 0, 1, math.MaxUint64))
	if ok, _ = fetched(t, freq, fr); ok.EndLocation != (wire.Location{Group: 1, Object: 2}) {
		t.Errorf("FETCH_OK ends at the group's last object, got %+v", ok.EndLocation)
	}
}

func TestFetchAscendingByDefault(t *testing.T) {
	// a FETCH naming no group order is served ascending even when the track's publisher prefers descending (§9.20.9)
	h := newHarness(t, relay.Options{LiveEdgeGroups: 5}, nil)
	props, _ := wire.AppendKVs(nil, []wire.KV{{
		Type: message.PropDefaultGroupOrder, Uint: uint64(message.GroupOrderDescending),
	}})
	h.o.props = props
	c := h.client()
	req, r := subscribe(t, c, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 1000, 1001)
	labeled := func(fr *rec) {
		t.Helper()
		fr.mu.Lock()
		defer fr.mu.Unlock()
		for i, fo := range fr.fetched {
			if want := fmt.Sprintf("g%d-o%d", fo.Group, fo.Object); fr.payloads[i] != want {
				t.Errorf("entry %d is labeled %s but carries %s", i, want, fr.payloads[i])
			}
		}
	}
	freq, fr := fetch(t, c, "video", fetchRange(1000, 0, 1001, math.MaxUint64))
	if _, payloads := fetched(t, freq, fr); !slices.Equal(payloads, groupPayloads(1000, 1001)) {
		t.Errorf("fetched %v, want ascending", payloads)
	}
	labeled(fr)
	p := fetchRange(1000, 0, 1001, math.MaxUint64)
	p.SetGroupOrder(message.GroupOrderDescending)
	freq, fr = fetch(t, c, "video", p)
	if _, payloads := fetched(t, freq, fr); !slices.Equal(payloads, groupPayloads(1001, 1000)) {
		t.Errorf("fetched %v, want descending as asked", payloads)
	}
	labeled(fr)
}

func testSpill(t *testing.T, c cache.Cache, stores *atomic.Int32) {
	h := newHarness(t, relay.Options{LiveEdgeGroups: 1, Spill: true}, c)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 0, 1, 2, 3)
	eventually(t, "spilled groups", func() bool { return stores.Load() >= 3 })
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 3, 2))
	_, payloads := fetched(t, freq, fr)
	if want := groupPayloads(0, 1, 2, 3); !slices.Equal(payloads, want) {
		t.Errorf("fetched %v, want %v", payloads, want)
	}
	if asks := h.o.fetchAsks(); len(asks) != 0 {
		t.Errorf("spilled groups serve from the cache, but the relay fetched %+v", asks)
	}
	// a group spilled through its end still names its last object when a range closes there
	freq, fr = fetch(t, cl, "video", fetchRange(1, 0, 1, math.MaxUint64))
	if ok, _ := fetched(t, freq, fr); ok.EndLocation != (wire.Location{Group: 1, Object: 2}) {
		t.Errorf("FETCH_OK ends at the spilled group's last object, got %+v", ok.EndLocation)
	}
}

func TestSpillToMemoryCache(t *testing.T) {
	c, stores := newMemoryCache()
	testSpill(t, c, stores)
}

func TestSpillToBBoltCache(t *testing.T) {
	c, stores := newBoltCache(t)
	testSpill(t, c, stores)
}

func TestGapFetch(t *testing.T) {
	c, stores := newMemoryCache()
	h := newHarness(t, relay.Options{LiveEdgeGroups: 1, Spill: true}, c)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 0)
	// group 1 exists at the origin but never reaches the relay's subscription
	h.o.store("video", 1, objs(groupPayloads(1)...)...)
	publishGroups(t, h, r, 2, 3)
	eventually(t, "spilled groups", func() bool { return stores.Load() >= 2 })
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 3, 2))
	_, payloads := fetched(t, freq, fr)
	if want := groupPayloads(0, 1, 2, 3); !slices.Equal(payloads, want) {
		t.Errorf("fetched %v, want %v", payloads, want)
	}
	asks := h.o.fetchAsks()
	if len(asks) != 1 || asks[0].start != (wire.Location{Group: 1}) || asks[0].end.Group != 1 {
		t.Fatalf("the relay fetches exactly the missing group, got %+v", asks)
	}
	// the fetched group is cached, so a second fetch needs nothing upstream
	eventually(t, "the fetched group to spill", func() bool { return stores.Load() >= 3 })
	freq, fr = fetch(t, cl, "video", fetchRange(1, 0, 1, math.MaxUint64))
	if _, payloads = fetched(t, freq, fr); !slices.Equal(payloads, groupPayloads(1)) {
		t.Errorf("refetched %v", payloads)
	}
	if asks = h.o.fetchAsks(); len(asks) != 1 {
		t.Errorf("a cached gap is not fetched again, got %+v", asks)
	}
}

func TestFetchDescending(t *testing.T) {
	h := newHarness(t, relay.Options{LiveEdgeGroups: 2}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	h.o.store("video", 0, objs(groupPayloads(0)...)...)
	publishGroups(t, h, r, 1, 2)
	p := fetchRange(0, 0, 2, 2)
	p.SetGroupOrder(message.GroupOrderDescending)
	freq, fr := fetch(t, cl, "video", p)
	_, payloads := fetched(t, freq, fr)
	want := slices.Concat(groupPayloads(2), groupPayloads(1), groupPayloads(0))
	if !slices.Equal(payloads, want) {
		t.Errorf("fetched %v, want %v", payloads, want)
	}
	if asks := h.o.fetchAsks(); len(asks) != 1 || asks[0].order != message.GroupOrderDescending {
		t.Errorf("the gap is fetched in the same order, got %+v", asks)
	}
}

func TestFetchWithoutSubscription(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	for g := range uint64(3) {
		h.o.store("video", g, objs(groupPayloads(g)...)...)
	}
	cl := h.client()
	freq, fr := fetch(t, cl, "video", message.Params{})
	ok, payloads := fetched(t, freq, fr)
	if ok.EndLocation != (wire.Location{Group: 2, Object: 2}) || h.o.statuses.Load() != 1 {
		t.Errorf("an unseen track's range comes from TRACK_STATUS: %+v, %d", ok.EndLocation, h.o.statuses.Load())
	}
	if want := groupPayloads(0, 1, 2); !slices.Equal(payloads, want) {
		t.Errorf("fetched %v, want %v", payloads, want)
	}
}

func TestFetchRefusals(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	freq, _ := fetch(t, cl, "nothing", message.Params{})
	m, _ := freq.Response(context.Background())
	if re, ok := m.(*message.RequestError); !ok || re.Code != message.RequestDoesNotExist {
		t.Errorf("an unknown track: %+v", m)
	}
	h.o.store("video", 0, objs("a")...)
	freq, _ = fetch(t, cl, "video", fetchRange(5, 0, 6, 0))
	m, _ = freq.Response(context.Background())
	if re, ok := m.(*message.RequestError); !ok || re.Code != message.RequestInvalidRange {
		t.Errorf("a range past Largest Object: %+v", m)
	}
}

func TestFetchTimedOutGap(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	h.o.store("video", 0, objs("a", "b")...)
	cl := h.client()
	p := fetchRange(0, 0, 0, 1)
	p.SetFillTimeout(0)
	freq, fr := fetch(t, cl, "video", p)
	fetched(t, freq, fr)
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.fetched) != 1 || fr.fetched[0].EndOfRange != message.RangeEndTimedOut ||
		fr.fetched[0].Group != 0 || fr.fetched[0].Object != 1 {
		t.Errorf("a zero fill timeout reports the gap as timed out: %+v", fr.fetched)
	}
	if asks := h.o.fetchAsks(); len(asks) != 0 {
		t.Errorf("nothing is fetched upstream: %+v", asks)
	}
}

func TestCacheExpiry(t *testing.T) {
	c, stores := newMemoryCache()
	h := newHarness(t, relay.Options{LiveEdgeGroups: 1, Spill: true}, c)
	props, _ := wire.AppendKVs(nil, []wire.KV{{Type: message.PropMaxCacheDuration, Uint: 200}})
	h.o.props = props
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	publishGroups(t, h, r, 0, 1)
	eventually(t, "the spilled group", func() bool { return stores.Load() >= 1 })
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 0, math.MaxUint64))
	fetched(t, freq, fr)
	if asks := h.o.fetchAsks(); len(asks) != 0 {
		t.Fatalf("a fresh group serves from the cache, got %+v", asks)
	}
	time.Sleep(250 * time.Millisecond)
	freq, fr = fetch(t, cl, "video", fetchRange(0, 0, 0, math.MaxUint64))
	if _, payloads := fetched(t, freq, fr); !slices.Equal(payloads, groupPayloads(0)) {
		t.Errorf("fetched %v", payloads)
	}
	if asks := h.o.fetchAsks(); len(asks) != 1 {
		t.Errorf("an expired group is unknown again and fetched upstream, got %+v", asks)
	}
}

func TestFill(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	first, frec := subscribe(t, cl, "video", message.Params{})
	subscribed(t, first)
	outs := h.o.stream("video", 5)
	for i, p := range []string{"o0", "o1"} {
		if err := writeObject(outs[0], pubObj{id: uint64(i), payload: []byte(p)}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "the live objects", func() bool { return frec.count() == 2 })
	var p message.Params
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
	fill := &message.Params{}
	fill.SetLocationFilter(message.LocationFilter{Type: message.FilterRelativeStart, StartGroup: 1})
	p.SetFill(fill)
	req, r := subscribe(t, cl, "video", p)
	subscribed(t, req)
	recv(t, r.fetchDone, "the fill stream")
	r.mu.Lock()
	payloads := slices.Clone(r.payloads)
	r.mu.Unlock()
	if !slices.Equal(payloads, []string{"o0", "o1"}) {
		t.Errorf("the fill delivers the current group from its start, got %v", payloads)
	}
	if err := writeObject(outs[0], pubObj{id: 2, payload: []byte("o2")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the next object by subscription", func() bool { return r.count() == 1 })

	// an update's fill arrives under the update's Request ID
	upd := message.Params{}
	upd.SetFill(fill)
	if _, err := req.Update(upd); err != nil {
		t.Fatal(err)
	}
	if m := recv(t, r.messages, "the update's answer"); m.Kind() != message.KindRequestOK {
		t.Errorf("got %+v", m)
	}
	recv(t, r.fetchDone, "the update's fill")

	// a fill whose range starts after Largest Object opens no stream
	empty := message.Params{}
	empty.SetFill(&message.Params{})
	if _, err := req.Update(empty); err != nil {
		t.Fatal(err)
	}
	recv(t, r.messages, "the second update's answer")
	select {
	case <-r.fetchDone:
		t.Error("an empty fill range opens no fill stream")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestFillOfEndedGroup(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	first, frec := subscribe(t, cl, "video", message.Params{})
	subscribed(t, first)
	publishGroups(t, h, frec, 0, 1)

	// group 1 ended before this subscription began, so an empty stream ends the group its fill delivers
	var p message.Params
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
	fill := &message.Params{}
	fill.SetLocationFilter(message.LocationFilter{Type: message.FilterRelativeStart, StartGroup: 1})
	p.SetFill(fill)
	req, r := subscribe(t, cl, "video", p)
	subscribed(t, req)
	recv(t, r.fetchDone, "the fill stream")
	eventually(t, "the empty stream ending group 1", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.Equal(r.tails, []uint64{1})
	})

	// a subscription starting at the next group is not told about the ended one
	var next message.Params
	next.SetLocationFilter(message.LocationFilter{Type: message.FilterRelativeStart})
	nreq, nr := subscribe(t, cl, "video", next)
	subscribed(t, nreq)
	publishGroups(t, h, frec, 2)
	eventually(t, "group 2 by the next-group subscription", func() bool { return nr.count() == 3 })
	nr.mu.Lock()
	defer nr.mu.Unlock()
	if len(nr.tails) != 0 {
		t.Errorf("a subscription past the ended group got tails %v", nr.tails)
	}
}

func TestTrackStatus(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	h.o.store("video", 4, objs("a", "b", "c")...)
	cl := h.client()
	req, err := cl.Open(context.Background(), &message.TrackStatus{Track: track("video")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := req.Response(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := m.(*message.RequestOK)
	if !isOK || ok.Params.LargestObject != (wire.Location{Group: 4, Object: 2}) {
		t.Errorf("got %+v", m)
	}
	req, _ = cl.Open(context.Background(), &message.TrackStatus{Track: track("missing")}, newRec(), nil)
	if m, _ = req.Response(context.Background()); m.Kind() != message.KindRequestError {
		t.Errorf("an unknown track: %+v", m)
	}
}

func TestSpillKeptApartAcrossRelays(t *testing.T) {
	caches := map[string]func(*testing.T) (cache.Cache, *atomic.Int32){
		"memory": func(*testing.T) (cache.Cache, *atomic.Int32) { return newMemoryCache() },
		"bbolt":  newBoltCache,
	}
	for name, newCache := range caches {
		t.Run(name, func(t *testing.T) {
			// two backends' relays share one cache, and their tracks share a scope and a name
			c, stores := newCache(t)
			hs := []*harness{
				newHarness(t, relay.Options{LiveEdgeGroups: 1, Spill: true, CacheNamespace: "tenant-a"}, c),
				newHarness(t, relay.Options{LiveEdgeGroups: 1, Spill: true, CacheNamespace: "tenant-b"}, c),
			}
			clients := make([]*session.Session, len(hs))
			for i, h := range hs {
				clients[i] = h.client()
				req, r := subscribe(t, clients[i], "video", message.Params{})
				subscribed(t, req)
				// group 0 leaves the one-group live edge, and spills, once group 1 arrives
				for g := range uint64(2) {
					before := r.count()
					h.o.publish(t, "video", g, objs(fmt.Sprintf("tenant%d-g%d", i, g))...)
					eventually(t, "the published object", func() bool { return r.count() == before+1 })
				}
				eventually(t, "the spilled group", func() bool { return stores.Load() >= int32(i+1) })
			}
			// the second relay's spill of group 0 neither replaced the first's nor became readable through it
			for i, cl := range clients {
				freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 0, math.MaxUint64))
				if _, payloads := fetched(t, freq, fr); !slices.Equal(payloads, []string{fmt.Sprintf("tenant%d-g0", i)}) {
					t.Errorf("relay %d fetched %v", i, payloads)
				}
				if asks := hs[i].o.fetchAsks(); len(asks) != 0 {
					t.Errorf("relay %d's own spilled group serves from the cache, but it fetched %+v", i, asks)
				}
			}
		})
	}
}
