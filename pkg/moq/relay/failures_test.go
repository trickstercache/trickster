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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

type deadUpstream struct {
	sess     *session.Session
	mu       sync.Mutex
	released []error
}

func newDeadUpstream(t *testing.T) *deadUpstream {
	t.Helper()
	// a session that completed SETUP and then closed, so every request opened on it fails at once
	cc, sc := memconn.Pair()
	server, _ := session.New(sc, session.Options{Role: session.RoleServer, Codec: c22(t), Handler: newOrigin()})
	client, _ := session.New(cc, session.Options{Role: session.RoleClient, Codec: c22(t)})
	errs := make(chan error, 2)
	go func() { errs <- server.Start(context.Background()) }()
	go func() { errs <- client.Start(context.Background()) }()
	for range 2 {
		if err := recv(t, errs, "session start"); err != nil {
			t.Fatal(err)
		}
	}
	_ = client.Close(message.CodeNoError, "")
	_ = server.Close(message.CodeNoError, "")
	return &deadUpstream{sess: client}
}

func (u *deadUpstream) Session(context.Context, relay.Scope, message.FullTrackName) (*session.Session, func(error),
	error,
) {
	return u.sess, func(err error) {
		u.mu.Lock()
		u.released = append(u.released, err)
		u.mu.Unlock()
	}, nil
}

func (u *deadUpstream) releases() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.released)
}

func deadHarness(t *testing.T, opts relay.Options) (*harness, *deadUpstream) {
	t.Helper()
	up := newDeadUpstream(t)
	h := &harness{t: t, scope: relay.Scope{Authority: "relay.test", Path: "/"}}
	h.relay = relay.New(opts, up, nil)
	t.Cleanup(h.relay.Close)
	return h, up
}

func TestOriginSessionClosed(t *testing.T) {
	m := newMetrics()
	h, up := deadHarness(t, relay.Options{Metrics: m})
	cl := h.session(nil)

	req, _ := subscribe(t, cl, "video", message.Params{})
	if _, ok := response(t, req).(*message.RequestError); !ok {
		t.Error("a SUBSCRIBE the origin cannot take is refused")
	}
	sreq, err := cl.Open(context.Background(), &message.TrackStatus{Track: track("video")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	refusedWith(t, sreq, message.RequestInternalError)
	if n := metricValue(m.Requests.WithLabelValues("track_status", "refused")); n != 1 {
		t.Errorf("track status refusals = %v", n)
	}

	// the namespace listener is accepted; its upstream attempts fail and give their sessions back
	listen(t, cl, ns("live"))
	eventually(t, "the failed attempts to be released", func() bool { return up.releases() >= 3 })
}

type decliningSink struct {
	asked chan struct{}
}

func (s *decliningSink) HandleGoAway(*session.Session, *message.GoAway) {}

func (s *decliningSink) HandleRequest(r *session.Request) {
	r.SetHandlers(newRec(), nil)
	if _, ok := r.Message().(*message.Publish); !ok {
		_ = r.Reject(message.RequestNotSupported, "", 0)
		return
	}
	_ = r.Reject(message.RequestUninterested, "", 0)
	s.asked <- struct{}{}
}

func TestOfferedTrackDeclined(t *testing.T) {
	m := newMetrics()
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{Metrics: m})
	sink := &decliningSink{asked: make(chan struct{}, 1)}
	st, err := h.session(sink).Open(context.Background(), &message.SubscribeTracks{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, st).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	published(t, h.session(nil), "video")
	recv(t, sink.asked, "the relay's PUBLISH")
	eventually(t, "the declined offer to be counted", func() bool {
		return metricValue(m.Requests.WithLabelValues("publish", "refused")) == 1
	})
	eventually(t, "the relay's subscription to end", func() bool {
		return metricValue(m.ActiveSubscriptions.WithLabelValues("downstream")) == 0
	})
}

func heldOrigin(t *testing.T, h *harness) {
	t.Helper()
	// the origin takes each SUBSCRIBE and answers none until the test ends
	h.o.hold = make(chan struct{})
	t.Cleanup(func() { close(h.o.hold) })
}

func TestRelayCloseRefusesPending(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	heldOrigin(t, h)
	req, _ := subscribe(t, h.client(), "video", message.Params{})
	eventually(t, "the upstream subscribe", func() bool { return h.o.subscribes.Load() == 1 })
	h.relay.Close()
	refusedWith(t, req, message.RequestInternalError)
}

func TestOriginLostWhilePending(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	heldOrigin(t, h)
	req, _ := subscribe(t, h.client(), "video", message.Params{})
	eventually(t, "the upstream subscribe", func() bool { return h.o.subscribes.Load() == 1 })
	h.up.down.Store(true)
	h.up.kill()
	if _, ok := response(t, req).(*message.RequestError); !ok {
		t.Error("a subscription whose origin is lost before answering is refused")
	}
}

func TestJoiningFetchOnPendingSubscription(t *testing.T) {
	h := draftHarness(t, 19)
	heldOrigin(t, h)
	cl := h.clientDraft(19)
	req, _ := subscribe(t, cl, "video", message.Params{})
	eventually(t, "the upstream subscribe", func() bool { return h.o.subscribes.Load() == 1 })
	join, err := cl.Open(context.Background(), &message.Fetch{
		Joining: &message.JoiningFetch{RequestID: req.RequestID(), Start: 1, Relative: true},
	}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	refusedWith(t, join, message.RequestInvalidJoiningID)
}

type junkCache struct {
	cache.Cache
	junk []byte
}

func (c junkCache) Retrieve(string) ([]byte, status.LookupStatus, error) {
	// a spilled group that is gone (nil) or unreadable (anything else)
	if c.junk == nil {
		return nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}
	return c.junk, status.LookupStatusHit, nil
}

func TestSpilledGroupUnreadable(t *testing.T) {
	for _, junk := range [][]byte{nil, []byte("not a group")} {
		c, stores := newBoltCache(t)
		h := newHarness(t, relay.Options{LiveEdgeGroups: 1, Spill: true}, junkCache{Cache: c, junk: junk})
		cl := h.client()
		req, r := subscribe(t, cl, "video", message.Params{})
		subscribed(t, req)
		publishGroups(t, h, r, 0, 1)
		eventually(t, "the evicted group to spill", func() bool { return stores.Load() == 1 })
		freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 0, math.MaxUint64))
		if _, got := fetched(t, freq, fr); !slices.Equal(got, groupPayloads(0)) {
			t.Errorf("fetched %v", got)
		}
		if len(h.o.fetchAsks()) != 1 {
			t.Error("a spilled group that cannot be read back is fetched upstream")
		}
	}
}

func TestFetchCanceledWhileObjectInProgress(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	req, _ := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	out := h.o.stream("video", 2)[0]
	t.Cleanup(func() { _ = out.Close() })
	if err := out.WriteObject(&message.SubgroupObject{ID: 0, PayloadLen: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("half")); err != nil {
		t.Fatal(err)
	}
	waitLargest(t, cl, "video", 2, 0)
	// the fetch waits on the unfinished object, and its cancellation resets the fetch stream
	freq, fr := fetch(t, cl, "video", fetchRange(2, 0, 2, 0))
	if _, ok := response(t, freq).(*message.FetchOK); !ok {
		t.Fatal("FETCH refused")
	}
	freq.Cancel(message.ResetCancelled)
	recv(t, fr.fetchDone, "the fetch stream to end")
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.payloads) != 0 {
		t.Errorf("a canceled fetch delivered %v", fr.payloads)
	}
}

func TestGapObjectTooLarge(t *testing.T) {
	h := newHarness(t, relay.Options{MaxObjectSize: 4}, nil)
	h.o.store("video", 0, objs("far too large")...)
	freq, fr := fetch(t, h.client(), "video", fetchRange(0, 0, 0, 0))
	if _, got := fetched(t, freq, fr); len(got) != 1 || got[0] != "" {
		t.Errorf("fetched %v", got)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.fetched[0].EndOfRange != message.RangeEndUnknown {
		t.Errorf("an oversize upstream object leaves the range unknown: %+v", fr.fetched)
	}
}

func TestTrackOfferedOnce(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	sink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	st, err := h.session(sink).Open(context.Background(), &message.SubscribeTracks{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, st).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	// a second publisher of the same track, as in a switchover, brings no second PUBLISH
	published(t, h.client(), "video")
	recv(t, sink.pubs, "the relay's PUBLISH")
	published(t, h.client(), "video")
	select {
	case r := <-sink.pubs:
		t.Errorf("the track was offered twice: %+v", r.Message())
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMidSubgroupJoinHeader(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	out, err := h.o.subscriptions("video")[0].OpenSubgroup(context.Background(), message.SubgroupHeader{
		Group: 0, Mode: message.SubgroupIDFirstObject, DefaultPriority: true, HasProperties: true,
	}, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	timeouts, _ := wire.AppendKVs(nil, []wire.KV{
		{Type: message.PropObjectDeliveryTimeout, Uint: 60000}, {Type: message.PropSubgroupDeliveryTimeout, Uint: 60000},
	})
	if err = writeObject(out, pubObj{id: 0, payload: []byte("a"), props: timeouts}); err != nil {
		t.Fatal(err)
	}
	if err = writeObject(out, pubObj{id: 1, payload: []byte("b")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first objects", func() bool { return r.count() == 2 })
	var p message.Params
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStart, StartGroup: 0, StartObject: 1})
	late, lr := subscribe(t, cl, "video", p)
	subscribed(t, late)
	if err = writeObject(out, pubObj{id: 2, payload: []byte("c")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the joiner's objects", func() bool { return lr.count() == 2 })
	// the joiner's stream starts after the subgroup's first object, so it names the subgroup and claims no first object
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if hd := lr.headers[0]; hd.Mode != message.SubgroupIDExplicit || hd.FirstObject || !hd.DefaultPriority {
		t.Errorf("joiner's header %+v", hd)
	}
	if got := payloads(lr.objects); !slices.Equal(got, []string{"b", "c"}) {
		t.Errorf("joiner received %v", got)
	}
}

func TestUnrelatedPrefixKept(t *testing.T) {
	h := originNamespaces(t, relay.Options{})
	_, other := listen(t, h.client(), ns("other"))
	first := recv(t, h.o.namespaces, "the SUBSCRIBE_NAMESPACE for other")
	listen(t, h.client(), ns("live", "studio"))
	recv(t, h.o.namespaces, "the narrow SUBSCRIBE_NAMESPACE")
	listen(t, h.client(), ns("live"))
	recv(t, h.o.namespaces, "the wide SUBSCRIBE_NAMESPACE")
	// the wide prefix replaces only the request it covers
	if err := first.Send(&message.Namespace{Suffix: ns("x")}); err != nil {
		t.Fatal(err)
	}
	expectNamespace(t, other, ns("x"))
}

func TestSourceEndsDuringResume(t *testing.T) {
	h := newHarness(t, relay.Options{ReconnectBackoff: 5 * time.Millisecond}, nil)
	h.o.endOnUpdate.Store(true)
	var paused message.Params
	paused.SetForward(false)
	preq, pr := subscribe(t, h.client(), "video", paused)
	subscribed(t, preq)
	// the resume's failure ends the only source: the relay subscribes again, the waiting subscriber is answered on
	// the new subscription, and the paused one never sees the failure
	areq, ar := subscribe(t, h.client(), "video", message.Params{})
	recv(t, h.o.updates, "the resume")
	subscribed(t, areq)
	eventually(t, "the upstream subscription again", func() bool { return len(h.o.subscriptions("video")) == 1 })
	if n := h.o.subscribes.Load(); n != 2 {
		t.Errorf("upstream subscribes = %d", n)
	}
	h.o.publish(t, "video", 0, objs("a")...)
	eventually(t, "the active subscriber's object", func() bool { return ar.count() == 1 })
	select {
	case m := <-pr.messages:
		t.Errorf("the paused subscriber saw %+v", m)
	default:
	}
}

func TestRenewalRefusedResubscribes(t *testing.T) {
	h := newHarness(t, relay.Options{ReconnectBackoff: 5 * time.Millisecond}, nil)
	h.o.subExpires = 40
	h.o.endOnUpdate.Store(true)
	req, r := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, req)
	// the origin ends the subscription when the relay renews it; the relay subscribes again, unseen downstream
	recv(t, h.o.updates, "the renewal")
	h.o.endOnUpdate.Store(false)
	eventually(t, "the upstream subscription again", func() bool { return h.o.subscribes.Load() == 2 })
	eventually(t, "forwarding upstream", func() bool { return len(h.o.subscriptions("video")) == 1 })
	h.o.publish(t, "video", 0, objs("a")...)
	eventually(t, "the object after the re-subscribe", func() bool { return r.count() == 1 })
	select {
	case m := <-r.messages:
		t.Errorf("the subscriber saw %+v", m)
	default:
	}
}

func TestPublisherServesFetchGap(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{LiveEdgeGroups: 1})
	// the publisher's session is also an origin, which answers the FETCH the relay sends it for a gap
	o := newOrigin()
	pub := published(t, h.session(o), "video")
	cl := h.session(nil)
	sreq, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, sreq)
	for g := range uint64(3) {
		o.store("video", g, objs(groupPayloads(g)...)...)
		sendGroup(t, pub, g, objs(groupPayloads(g)...)...)
		eventually(t, "the group", func() bool { return r.count() == int(g+1)*3 })
	}
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 0, math.MaxUint64))
	if _, got := fetched(t, freq, fr); !slices.Equal(got, groupPayloads(0)) {
		t.Errorf("fetched %v", got)
	}
	if len(o.fetchAsks()) != 1 {
		t.Error("the evicted group is fetched from its publisher")
	}
}

func TestOriginEndsNamespaceSubscription(t *testing.T) {
	h := originNamespaces(t, relay.Options{ReconnectBackoff: 5 * time.Millisecond})
	listen(t, h.client(), ns("live"))
	up := recv(t, h.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE")
	// the listener still wants the prefix, so the relay asks again
	_ = up.CloseSend()
	again := recv(t, h.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE, again")
	if m := again.Message().(*message.SubscribeNamespace); !m.Prefix.Equal(ns("live")) {
		t.Errorf("asked again for %v", m.Prefix)
	}
}

func TestFetchMergesUnheldGroups(t *testing.T) {
	h := newHarness(t, relay.Options{LiveEdgeGroups: 5}, nil)
	for g := range uint64(3) {
		h.o.store("video", g, objs(groupPayloads(g)...)...)
	}
	cl := h.client()
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	// the relay holds only the start of group 2, so the groups before it and group 2 form one gap
	out := h.o.stream("video", 2)[0]
	t.Cleanup(func() { _ = out.Close() })
	if err := writeObject(out, pubObj{id: 0, payload: []byte("g2-o0")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the open group's object", func() bool { return r.count() == 1 })
	for i, order := range []message.GroupOrder{message.GroupOrderAscending, message.GroupOrderDescending} {
		p := fetchRange(0, 0, 2, math.MaxUint64)
		p.SetGroupOrder(order)
		freq, fr := fetch(t, cl, "video", p)
		if _, got := fetched(t, freq, fr); len(got) != 9 {
			t.Errorf("order %d fetched %v", order, got)
		}
		asks := h.o.fetchAsks()
		if len(asks) != i+1 || asks[i].start.Group != 0 || asks[i].end.Group != 2 || asks[i].order != order {
			t.Errorf("order %d: upstream fetches %+v", order, asks)
		}
	}
}

type streamlessConn struct {
	*memconn.Conn
	refuse atomic.Bool
}

func (c *streamlessConn) OpenStreamSync(ctx context.Context) (session.Stream, error) {
	if c.refuse.Load() {
		return nil, errors.New("no stream credit")
	}
	return c.Conn.OpenStreamSync(ctx)
}

func TestOfferedTrackCannotOpen(t *testing.T) {
	m := newMetrics()
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{Metrics: m})
	var conn *streamlessConn
	viewer := h.connect(nil, func(c *memconn.Conn) session.Conn {
		conn = &streamlessConn{Conn: c}
		return conn
	})
	st, err := viewer.Open(context.Background(), &message.SubscribeTracks{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := newRec()
	st.SetHandlers(r, nil)
	if _, ok := response(t, st).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	// a PUBLISH the relay cannot open toward the subscriber is reported as skipped (§3.6.3)
	conn.refuse.Store(true)
	published(t, h.session(nil), "video")
	if sk, ok := recv(t, r.messages, "PUBLISH_SKIPPED").(*message.PublishSkipped); !ok || string(sk.TrackName) != "video" {
		t.Errorf("got %+v", sk)
	}
	if n := metricValue(m.Requests.WithLabelValues("publish", "refused")); n != 1 {
		t.Errorf("refused offers = %v", n)
	}
}

type gatedConn struct {
	*memconn.Conn
	gated   atomic.Bool
	entered chan struct{}
	gate    chan struct{}
}

func (c *gatedConn) OpenStreamSync(ctx context.Context) (session.Stream, error) {
	if c.gated.Load() {
		c.entered <- struct{}{}
		<-c.gate
	}
	return c.Conn.OpenStreamSync(ctx)
}

type gatedUpstream struct {
	sess *session.Session
}

func (u gatedUpstream) Session(context.Context, relay.Scope, message.FullTrackName) (*session.Session, func(error),
	error,
) {
	return u.sess, nil, nil
}

func newGatedUpstream(t *testing.T, o *origin) (gatedUpstream, *gatedConn) {
	t.Helper()
	// an origin session whose next request stream opens only when the test lets it
	cc, sc := memconn.Pair()
	gc := &gatedConn{Conn: cc, entered: make(chan struct{}, 1), gate: make(chan struct{})}
	server, _ := session.New(sc, session.Options{Role: session.RoleServer, Codec: c22(t), Handler: o})
	client, _ := session.New(gc, session.Options{Role: session.RoleClient, Codec: c22(t)})
	errs := make(chan error, 2)
	go func() { errs <- server.Start(context.Background()) }()
	go func() { errs <- client.Start(context.Background()) }()
	for range 2 {
		if err := recv(t, errs, "session start"); err != nil {
			t.Fatal(err)
		}
	}
	gc.gated.Store(true)
	t.Cleanup(func() {
		_ = client.Close(message.CodeNoError, "")
		_ = server.Close(message.CodeNoError, "")
	})
	return gatedUpstream{sess: client}, gc
}

func TestPrefixStoppedWhileOpening(t *testing.T) {
	o := newOrigin()
	o.namespaces = make(chan *session.Request, 4)
	up, gc := newGatedUpstream(t, o)
	h := &harness{t: t, scope: relay.Scope{Authority: "relay.test", Path: "/"}}
	h.relay = relay.New(relay.Options{Linger: 10 * time.Millisecond}, up, nil)
	t.Cleanup(h.relay.Close)
	req, _ := listen(t, h.session(nil), ns("live"))
	recv(t, gc.entered, "the upstream request opening")
	// the listener leaves and its prefix subscription stops before the upstream request is out
	req.Cancel(message.ResetCancelled)
	time.Sleep(50 * time.Millisecond)
	close(gc.gate)
	sent := recv(t, o.namespaces, "the upstream SUBSCRIBE_NAMESPACE")
	eventually(t, "the stopped request to be canceled", func() bool { return sent.Err() != nil })
}

func TestSubscriberCannotOpenStreams(t *testing.T) {
	m := newMetrics()
	h := newHarness(t, relay.Options{LiveEdgeGroups: 5, Metrics: m}, nil)
	var conn *uniRefusingConn
	cl := h.connect(nil, func(c *memconn.Conn) session.Conn {
		conn = &uniRefusingConn{Conn: c}
		return conn
	})
	req, r := subscribe(t, cl, "video", message.Params{})
	subscribed(t, req)
	conn.refuse.Store(true)
	// neither the subgroup stream nor the fetch stream opens, so nothing arrives and the fetch is reset
	h.o.publish(t, "video", 0, objs("a")...)
	waitLargest(t, cl, "video", 0, 0)
	freq, fr := fetch(t, cl, "video", fetchRange(0, 0, 0, 0))
	if _, ok := response(t, freq).(*message.FetchOK); !ok {
		t.Fatal("FETCH refused")
	}
	time.Sleep(20 * time.Millisecond)
	if r.count() != 0 || len(fr.fetchDone) != 0 {
		t.Error("no stream could open, so nothing is delivered")
	}
}

type uniRefusingConn struct {
	*memconn.Conn
	refuse atomic.Bool
}

func (c *uniRefusingConn) OpenUniStreamSync(ctx context.Context) (session.SendStream, error) {
	if c.refuse.Load() {
		return nil, errors.New("no stream credit")
	}
	return c.Conn.OpenUniStreamSync(ctx)
}
