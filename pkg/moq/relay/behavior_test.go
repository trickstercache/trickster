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
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestDatagrams(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	h.o.datagrams = true
	cl := h.client()
	var recs []*rec
	for range 2 {
		req, r := subscribe(t, cl, "audio", message.Params{})
		subscribed(t, req)
		recs = append(recs, r)
	}
	h.o.publish(t, "audio", 3, objs("a", "b")...)
	for _, r := range recs {
		var gotIDs []uint64
		for range 2 {
			d := recv(t, r.datagrams, "datagram")
			gotIDs = append(gotIDs, d.id)
		}
		slices.Sort(gotIDs)
		if !slices.Equal(gotIDs, []uint64{0, 1}) {
			t.Errorf("datagrams %v", gotIDs)
		}
	}
	freq, fr := fetch(t, cl, "audio", fetchRange(3, 0, 3, 1))
	_, payloads := fetched(t, freq, fr)
	if !slices.Equal(payloads, []string{"a", "b"}) {
		t.Errorf("fetched %v", payloads)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if !fr.fetched[0].Datagram {
		t.Error("a fetched datagram object keeps its delivery mode")
	}
	if len(h.o.fetchAsks()) != 0 {
		t.Error("datagram objects serve from the live edge")
	}
}

func TestDuplicatesAndMalformed(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	h.o.publish(t, "video", 1, objs("a", "b", "c")...)
	h.o.publish(t, "video", 1, objs("a", "b", "c")...)
	h.o.publish(t, "video", 2, objs("d")...)
	eventually(t, "the objects", func() bool { return r.count() >= 4 })
	time.Sleep(20 * time.Millisecond)
	if n := r.count(); n != 4 {
		t.Errorf("a duplicate copy is forwarded once, got %d objects", n)
	}
	// the same object with another payload makes the track malformed
	h.o.publish(t, "video", 2, objs("x")...)
	m := recv(t, r.messages, "PUBLISH_DONE")
	if done, ok := m.(*message.PublishDone); !ok || done.Code != message.DoneMalformedTrack {
		t.Fatalf("got %+v", m)
	}
	recv(t, h.o.ended, "the upstream subscription to end")
}

func TestStreamPriorities(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	pc := &prioConn{}
	cl := h.clientWith(func(c *memconn.Conn) session.Conn { pc.Conn = c; return pc })
	var p message.Params
	p.SetSubscriberPriority(10)
	p.SetGroupOrder(message.GroupOrderDescending)
	req, r := subscribe(t, cl, "video", p)
	subscribed(t, req)
	h.o.publish(t, "video", 1, objs("a")...)
	eventually(t, "the object", func() bool { return r.count() == 1 })
	want := prio{urgency: session.Urgency(10, 1), incremental: true}
	if seen := pc.seen(); len(seen) == 0 || seen[len(seen)-1] != want {
		t.Errorf("stream priorities %+v, want %+v", seen, want)
	}
}

func TestLiveEdgeByteBudget(t *testing.T) {
	c, stores := newMemoryCache()
	h := newHarness(t, relay.Options{LiveEdgeGroups: 100, LiveEdgeBytes: 2000, Spill: true}, c)
	req, r := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	big := string(bytes.Repeat([]byte("x"), 400))
	for g := range uint64(5) {
		before := r.count()
		h.o.publish(t, "video", g, objs(big, big, big)...)
		eventually(t, "the group", func() bool { return r.count() == before+3 })
	}
	eventually(t, "groups evicted over the byte budget", func() bool { return stores.Load() >= 3 })
}

func newMetrics() *relay.Metrics {
	return &relay.Metrics{
		Requests:            prometheus.NewCounterVec(prometheus.CounterOpts{Name: "r"}, []string{"type", "result"}),
		ActiveSubscriptions: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "s"}, []string{"direction"}),
		Objects:             prometheus.NewCounterVec(prometheus.CounterOpts{Name: "o"}, []string{"direction", "delivery"}),
		Bytes:               prometheus.NewCounterVec(prometheus.CounterOpts{Name: "b"}, []string{"direction"}),
		DroppedObjects:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "d"}, []string{"reason"}),
		FetchObjects:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "f"}, []string{"source"}),
		LiveEdgeBytes:       prometheus.NewGauge(prometheus.GaugeOpts{Name: "l"}),
		Errors:              prometheus.NewCounterVec(prometheus.CounterOpts{Name: "e"}, []string{"class"}),
	}
}

func TestMetrics(t *testing.T) {
	m := newMetrics()
	h := newHarness(t, relay.Options{Metrics: m}, nil)
	cl := h.client()
	var recs []*rec
	for range 2 {
		req, r := subscribe(t, cl, "video", message.Params{})
		subscribed(t, req)
		recs = append(recs, r)
	}
	h.o.publish(t, "video", 0, objs("abc", "de")...)
	for _, r := range recs {
		eventually(t, "the objects", func() bool { return r.count() == 2 })
	}
	eventually(t, "the outbound count", func() bool {
		return testutil.ToFloat64(m.Objects.WithLabelValues("out", "stream")) == 4
	})
	checks := []struct {
		name string
		c    prometheus.Collector
		want float64
	}{
		{"objects in", m.Objects.WithLabelValues("in", "stream"), 2},
		{"bytes in", m.Bytes.WithLabelValues("in"), 5},
		{"bytes out", m.Bytes.WithLabelValues("out"), 10},
		{"subscribes", m.Requests.WithLabelValues("subscribe", "ok"), 2},
		{"downstream", m.ActiveSubscriptions.WithLabelValues("downstream"), 2},
		{"upstream", m.ActiveSubscriptions.WithLabelValues("upstream"), 1},
		{"live edge", m.LiveEdgeBytes, 5},
	}
	for _, c := range checks {
		if got := testutil.ToFloat64(c.c); got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 0, 1))
	fetched(t, freq, fr)
	if got := testutil.ToFloat64(m.FetchObjects.WithLabelValues("live_edge")); got != 2 {
		t.Errorf("fetched from the live edge %v", got)
	}
}

func TestUnsupportedRequest(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	req, err := h.client().Open(context.Background(), &message.PublishNamespace{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := req.Response(context.Background())
	if re, ok := m.(*message.RequestError); !ok || re.Code != message.RequestNotSupported {
		t.Errorf("got %+v", m)
	}
}

func TestRelayCloseEndsUpstream(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	req, _ := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	h.relay.Close()
	recv(t, h.o.ended, "the upstream subscription to end")
}

func TestSubscriptionUpdates(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	h.o.publish(t, "video", 0, objs("a")...)
	eventually(t, "the first object", func() bool { return r.count() == 1 })
	var narrow message.Params
	narrow.SetSubscriberPriority(200)
	narrow.SetLocationFilter(message.LocationFilter{
		Type: message.FilterAbsoluteStartGroupEnd, StartGroup: 0,
		EndGroupDelta: 1,
	})
	narrow.RangeFilters = []message.RangeFilter{{Type: message.ParamObjectIDFilter, Ranges: []wire.Range{{Start: 0, End: 0}}}}
	narrow.SetObjectDeliveryTimeout(60000)
	narrow.SetSubgroupDeliveryTimeout(60000)
	if _, err := req.Update(narrow); err != nil {
		t.Fatal(err)
	}
	ok := recv(t, r.messages, "the update's answer").(*message.RequestOK)
	if !ok.Params.Has(message.ParamLargestObject) || ok.Params.LargestObject != (wire.Location{}) {
		t.Errorf("REQUEST_OK carries Largest Object: %+v", ok.Params)
	}
	h.o.publish(t, "video", 1, objs("b", "c")...)
	h.o.publish(t, "video", 2, objs("d")...)
	eventually(t, "the narrowed objects", func() bool { return r.count() == 2 })
	time.Sleep(20 * time.Millisecond)
	if g := r.received(); !slices.Equal(g, []got{{0, 0, "a"}, {1, 0, "b"}}) {
		t.Errorf("after narrowing got %+v", g)
	}
}

func TestPausedSubscriberResumes(t *testing.T) {
	h := newHarness(t, relay.Options{Prewarm: true}, nil)
	cl := h.client()
	var paused message.Params
	paused.SetForward(false)
	req, r := subscribe(t, cl, "video", paused)
	subscribed(t, req)
	h.o.publish(t, "video", 0, objs("missed")...)
	time.Sleep(20 * time.Millisecond)
	if r.count() != 0 {
		t.Fatal("a paused subscriber receives nothing")
	}
	var resume message.Params
	resume.SetForward(true)
	if _, err := req.Update(resume); err != nil {
		t.Fatal(err)
	}
	recv(t, r.messages, "the update's answer")
	h.o.publish(t, "video", 1, objs("seen")...)
	eventually(t, "the object after resuming", func() bool { return r.count() == 1 })
	if g := r.received(); g[0].payload != "seen" {
		t.Errorf("got %+v", g)
	}
}

func metricValue(c prometheus.Collector) float64 {
	return testutil.ToFloat64(c)
}
