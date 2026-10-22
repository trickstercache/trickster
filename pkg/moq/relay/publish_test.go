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
	"slices"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	pubToken = "publisher-token"
	subToken = "subscriber-token"
)

func ingestHarness(t *testing.T, a relay.Access, opts relay.Options) *harness {
	t.Helper()
	h := &harness{t: t, scope: relay.Scope{Authority: "relay.test", Path: "/"}}
	h.relay = relay.New(opts, nil, nil)
	h.relay.SetAccess(a)
	t.Cleanup(h.relay.Close)
	return h
}

func (h *harness) session(handler session.Handler, tokens ...string) *session.Session {
	h.t.Helper()
	// the client serves the relay's requests with handler, and its SETUP carries tokens
	cc, sc := memconn.Pair()
	server, err := session.New(sc, session.Options{
		Role: session.RoleServer, Codec: c22(h.t), Handler: h.relay.Handler(h.scope),
		Setup: message.Setup{MaxRequestUpdates: 16},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	setup := message.Setup{Path: h.scope.Path, HasPath: true, Authority: h.scope.Authority, HasAuthority: true}
	for _, tok := range tokens {
		setup.AuthTokens = append(setup.AuthTokens, wire.Token{AliasType: wire.TokenUseValue, Value: []byte(tok)})
	}
	client, err := session.New(cc, session.Options{
		Role: session.RoleClient, Codec: c22(h.t), Handler: handler,
		Setup: setup,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { errs <- server.Start(context.Background()) }()
	go func() { errs <- client.Start(context.Background()) }()
	for range 2 {
		if err := recv(h.t, errs, "session start"); err != nil {
			h.t.Fatal(err)
		}
	}
	h.t.Cleanup(func() {
		_ = client.Close(message.CodeNoError, "")
		_ = server.Close(message.CodeNoError, "")
	})
	return client
}

func response(t *testing.T, req *session.Request) message.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	m, err := req.Response(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func refusedWith(t *testing.T, req *session.Request, code message.RequestCode) {
	t.Helper()
	if re, ok := response(t, req).(*message.RequestError); !ok || re.Code != code {
		t.Fatalf("want REQUEST_ERROR %#x", code)
	}
}

func startPublish(t *testing.T, c *session.Session, name string, p message.Params,
	props message.Properties,
) (*session.Request, *rec) {
	t.Helper()
	r := newRec()
	req, err := c.Open(context.Background(), &message.Publish{
		Track: track(name), TrackAlias: c.NewTrackAlias(), Params: p, TrackProperties: props,
	}, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req, r
}

func published(t *testing.T, c *session.Session, name string) *session.Request {
	t.Helper()
	req, _ := startPublish(t, c, name, message.Params{}, nil)
	if _, ok := response(t, req).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH refused")
	}
	return req
}

func sendGroup(t *testing.T, req *session.Request, group uint64, os ...pubObj) {
	t.Helper()
	out, err := req.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: group, Priority: 1, EndOfGroup: true},
		3, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, ob := range os {
		if err = writeObject(out, ob); err != nil {
			t.Fatal(err)
		}
	}
	_ = out.Close()
}

func payloads(gs []got) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.payload
	}
	return out
}

func TestIngestWithoutOrigin(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	pub := published(t, h.client(), "video")
	sreq, srec := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, sreq)
	sendGroup(t, pub, 0, objs("key", "delta")...)
	eventually(t, "the published objects", func() bool { return srec.count() == 2 })
	if got := payloads(srec.received()); !slices.Equal(got, []string{"key", "delta"}) {
		t.Errorf("objects %v", got)
	}
	// TRACK_STATUS and FETCH are answered from what was published
	freq, fr := fetch(t, h.client(), "video", fetchRange(0, 0, 0, 1))
	if _, got := fetched(t, freq, fr); !slices.Equal(got, []string{"key", "delta"}) {
		t.Errorf("fetched %v", got)
	}
	// a publisher that ends the track ends every subscription with its code
	_ = pub.Send(&message.PublishDone{Code: message.DoneTrackEnded, StreamCount: pub.StreamCount()})
	_ = pub.CloseSend()
	for {
		m := recv(t, srec.messages, "PUBLISH_DONE")
		if pd, ok := m.(*message.PublishDone); ok {
			if pd.Code != message.DoneTrackEnded {
				t.Errorf("subscription ended with %+v", pd)
			}
			break
		}
	}
}

func TestPublishingRefusals(t *testing.T) {
	closed := ingestHarness(t, relay.Access{}, relay.Options{})
	req, _ := startPublish(t, closed.client(), "video", message.Params{}, nil)
	refusedWith(t, req, message.RequestNotSupported)

	h := ingestHarness(t, relay.Access{
		Publish: true, Namespaces: []wire.Namespace{ns("live", "studio")},
		MaxPublishersPerTrack: 1,
	}, relay.Options{})
	c := h.client()
	req, _ = startPublish(t, c, "video", message.Params{}, nil)
	refusedWith(t, req, message.RequestUnauthorized)
	ns1, err := c.Open(context.Background(), &message.PublishNamespace{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	refusedWith(t, ns1, message.RequestUnauthorized)

	studio := func(name string) message.FullTrackName {
		return message.FullTrackName{Namespace: ns("live", "studio"), Name: []byte(name)}
	}
	open := func(name string, props message.Properties) *session.Request {
		r, err := c.Open(context.Background(), &message.Publish{
			Track: studio(name), TrackAlias: c.NewTrackAlias(), TrackProperties: props,
		}, newRec(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if _, ok := response(t, open("cam1", nil)).(*message.RequestOK); !ok {
		t.Fatal("a publisher within the allowed namespace is accepted")
	}
	refusedWith(t, open("cam1", nil), message.RequestExcessiveLoad)
	mandatory, _ := wire.AppendKVs(nil, []wire.KV{{Type: message.PropMandatoryMin, Bytes: []byte("x")}})
	refusedWith(t, open("cam2", mandatory), message.RequestUnsupportedExtension)
}

func TestRendezvous(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	sub := h.client()
	req, _ := subscribe(t, sub, "audio", message.Params{})
	refusedWith(t, req, message.RequestDoesNotExist)

	var short message.Params
	short.SetRendezvousTimeout(20)
	req, _ = subscribe(t, sub, "audio", short)
	refusedWith(t, req, message.RequestTimeout)

	var long message.Params
	long.SetRendezvousTimeout(uint64(wait / time.Millisecond))
	req, r := subscribe(t, sub, "audio", long)
	pub := published(t, h.client(), "audio")
	subscribed(t, req)
	sendGroup(t, pub, 3, objs("late")...)
	eventually(t, "an object from the publisher that arrived", func() bool { return r.count() == 1 })
}

func TestPublisherSwitchover(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	first := published(t, h.client(), "video")
	sreq, srec := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, sreq)
	second := published(t, h.client(), "video")
	// §7.6.1: both publish for a while; the relay forwards each object once
	sendGroup(t, first, 0, objs("a", "b")...)
	eventually(t, "the first publisher's objects", func() bool { return srec.count() == 2 })
	sendGroup(t, second, 0, objs("a", "b")...)
	sendGroup(t, second, 1, objs("c")...)
	eventually(t, "the second publisher's new object", func() bool { return srec.count() == 3 })
	first.Cancel(message.ResetCancelled)
	sendGroup(t, second, 2, objs("d")...)
	eventually(t, "objects once the first publisher left", func() bool { return srec.count() == 4 })
	if got := payloads(srec.received()); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("objects %v", got)
	}
	select {
	case m := <-srec.messages:
		t.Errorf("the subscription sees no change of publisher, got %+v", m)
	default:
	}
}

func TestPublisherReconnects(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{ReconnectBackoff: 20 * time.Millisecond})
	pubSess := h.client()
	pub := published(t, pubSess, "video")
	sreq, srec := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, sreq)
	sendGroup(t, pub, 0, objs("before")...)
	eventually(t, "an object before the publisher dropped", func() bool { return srec.count() == 1 })
	// a publisher whose session fails comes back on a new one; the subscription waits for it
	_ = pubSess.Close(message.CodeInternalError, "network change")
	again := published(t, h.client(), "video")
	sendGroup(t, again, 1, objs("after")...)
	eventually(t, "an object from the returning publisher", func() bool { return srec.count() == 2 })
	if sreq.Err() != nil {
		t.Errorf("the subscription survived: %v", sreq.Err())
	}
}

func TestPausedPublisherResumed(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	var paused message.Params
	paused.SetForward(false)
	pub, prec := startPublish(t, h.client(), "video", paused, nil)
	if _, ok := response(t, pub).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH refused")
	}
	sreq, _ := subscribe(t, h.client(), "video", message.Params{})
	// §7.6: a forwarding subscriber makes the relay resume a paused publisher
	u, ok := recv(t, prec.messages, "the resuming update").(*message.RequestUpdate)
	if !ok || !u.Params.Has(message.ParamForward) || !u.Params.Forward {
		t.Fatalf("got %+v", u)
	}
	if err := pub.Send(&message.RequestOK{}); err != nil {
		t.Fatal(err)
	}
	subscribed(t, sreq)
}

func TestNamespacePublisher(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	watcher := h.client()
	all, err := watcher.Open(context.Background(), &message.SubscribeNamespace{}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	watched := newRec()
	all.SetHandlers(watched, nil)
	if _, ok := response(t, all).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_NAMESPACE refused")
	}
	overlap, _ := watcher.Open(context.Background(), &message.SubscribeNamespace{Prefix: ns("live")}, newRec(), nil)
	refusedWith(t, overlap, message.RequestPrefixOverlap)

	o := newOrigin()
	pubSess := h.session(o)
	ann, err := pubSess.Open(context.Background(), &message.PublishNamespace{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, ann).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH_NAMESPACE refused")
	}
	if n, ok := recv(t, watched.messages, "NAMESPACE").(*message.Namespace); !ok || !n.Suffix.Equal(ns("live")) {
		t.Fatalf("got %+v", n)
	}
	// a SUBSCRIBE under the namespace is routed to its publisher (§7.6)
	sreq, srec := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, sreq)
	o.publish(t, "video", 0, objs("routed")...)
	eventually(t, "an object from the namespace publisher", func() bool { return srec.count() == 1 })

	ann.Cancel(message.ResetCancelled)
	if d, ok := recv(t, watched.messages, "NAMESPACE_DONE").(*message.NamespaceDone); !ok || !d.Suffix.Equal(ns("live")) {
		t.Fatalf("got %+v", d)
	}
}

func TestNamespacePublisherArrivesLate(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	var p message.Params
	p.SetRendezvousTimeout(uint64(wait / time.Millisecond))
	sreq, srec := subscribe(t, h.client(), "video", p)
	o := newOrigin()
	ann, err := h.session(o).Open(context.Background(), &message.PublishNamespace{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, ann).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH_NAMESPACE refused")
	}
	subscribed(t, sreq)
	o.publish(t, "video", 0, objs("found")...)
	eventually(t, "an object from the late publisher", func() bool { return srec.count() == 1 })
}

type tracksSink struct {
	pubs chan *session.Request
	rec  *rec
}

func (s *tracksSink) HandleGoAway(*session.Session, *message.GoAway) {}

func (s *tracksSink) HandleRequest(r *session.Request) {
	if _, ok := r.Message().(*message.Publish); !ok {
		r.SetHandlers(newRec(), nil)
		_ = r.Reject(message.RequestNotSupported, "", 0)
		return
	}
	r.SetHandlers(s.rec, s.rec)
	_ = r.Send(&message.RequestOK{})
	s.pubs <- r
}

func TestSubscribeTracks(t *testing.T) {
	m := newMetrics()
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{Metrics: m})
	sink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	viewer := h.session(sink)
	st, err := viewer.Open(context.Background(), &message.SubscribeTracks{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, st).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	// a publisher's own tracks are not offered back to it
	selfSink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	self := h.session(selfSink)
	own, err := self.Open(context.Background(), &message.SubscribeTracks{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, own).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	pub := published(t, self, "video")
	if testutil.ToFloat64(m.Publishers) != 1 {
		t.Error("the publisher is counted")
	}
	offered := recv(t, sink.pubs, "the relay's PUBLISH")
	if pm, ok := offered.Message().(*message.Publish); !ok || string(pm.Track.Name) != "video" {
		t.Fatalf("offered %+v", offered.Message())
	}
	// the relay's subscription forwards from its PUBLISH on
	eventually(t, "the relay's subscription", func() bool {
		return testutil.ToFloat64(m.ActiveSubscriptions.WithLabelValues("downstream")) == 1
	})
	sendGroup(t, pub, 0, objs("pushed")...)
	eventually(t, "an object on the relay's PUBLISH", func() bool { return sink.rec.count() == 1 })
	select {
	case r := <-selfSink.pubs:
		t.Errorf("the publisher was offered its own track: %+v", r.Message())
	default:
	}
}

type tokenAuth map[string]relay.Operation

func (a tokenAuth) Authorize(op relay.Operation, _ wire.Namespace, tokens []wire.Token) bool {
	for _, tok := range tokens {
		if got, ok := a[string(tok.Value)]; ok && got == op {
			return true
		}
	}
	return false
}

func TestAuthorization(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true, Authorizer: tokenAuth{
		pubToken: relay.OpPublish,
		subToken: relay.OpSubscribe,
	}}, relay.Options{})
	anon := h.client()
	req, _ := startPublish(t, anon, "video", message.Params{}, nil)
	refusedWith(t, req, message.RequestUnauthorized)
	req, _ = subscribe(t, anon, "video", message.Params{})
	refusedWith(t, req, message.RequestUnauthorized)
	freq, _ := fetch(t, anon, "video", fetchRange(0, 0, 0, 1))
	refusedWith(t, freq, message.RequestUnauthorized)
	for _, m := range []message.Message{
		&message.SubscribeNamespace{}, &message.SubscribeTracks{},
		&message.TrackStatus{Track: track("video")},
	} {
		r, err := anon.Open(context.Background(), m, newRec(), nil)
		if err != nil {
			t.Fatal(err)
		}
		refusedWith(t, r, message.RequestUnauthorized)
	}

	// a token in SETUP covers the session's requests; one in a request covers that request
	pub := published(t, h.session(nil, pubToken), "video")
	var p message.Params
	p.AuthTokens = []wire.Token{{AliasType: wire.TokenUseValue, Value: []byte(subToken)}}
	sreq, srec := subscribe(t, anon, "video", p)
	subscribed(t, sreq)
	sendGroup(t, pub, 0, objs("authorized")...)
	eventually(t, "an object for the authorized subscriber", func() bool { return srec.count() == 1 })
	wrong, _ := startPublish(t, h.session(nil, subToken), "audio", message.Params{}, nil)
	refusedWith(t, wrong, message.RequestUnauthorized)
}

func TestMalformedPublisherTrack(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	first := published(t, h.client(), "video")
	second := published(t, h.client(), "video")
	sreq, srec := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, sreq)
	sendGroup(t, first, 0, objs("a")...)
	eventually(t, "an object", func() bool { return srec.count() == 1 })
	// §7.1: the same object with another payload makes the track malformed
	sendGroup(t, second, 0, objs("not-a")...)
	for {
		m := recv(t, srec.messages, "PUBLISH_DONE")
		if pd, ok := m.(*message.PublishDone); ok {
			if pd.Code != message.DoneMalformedTrack {
				t.Errorf("subscription ended with %+v", pd)
			}
			break
		}
	}
	if err := recv(t, srec.closes, "the subscription's end"); err != nil && !errors.Is(err, session.ErrRequestEnded) {
		t.Logf("ended with %v", err)
	}
}

func TestMandatoryTrackPropertyUpstream(t *testing.T) {
	h := newHarness(t, relay.Options{}, nil)
	h.o.props, _ = wire.AppendKVs(nil, []wire.KV{{Type: message.PropMandatoryMin, Bytes: []byte("x")}})
	req, _ := subscribe(t, h.client(), "video", message.Params{})
	refusedWith(t, req, message.RequestUnsupportedExtension)
}

func TestNamespacePublisherJoinsSuppliedTrack(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	pub := published(t, h.client(), "video")
	sreq, srec := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, sreq)
	// §7.6: a namespace publisher matching a wanted track is subscribed to as well, and its objects deduped
	o := newOrigin()
	ann, err := h.session(o).Open(context.Background(), &message.PublishNamespace{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, ann).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH_NAMESPACE refused")
	}
	eventually(t, "the relay's SUBSCRIBE to the namespace publisher", func() bool {
		return len(o.subscriptions("video")) == 1
	})
	sendGroup(t, pub, 0, objs("a")...)
	o.publish(t, "video", 0, objs("a")...)
	o.publish(t, "video", 1, objs("b")...)
	eventually(t, "objects from both publishers", func() bool { return srec.count() == 2 })
	if got := payloads(srec.received()); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("objects %v", got)
	}
}

func TestPublisherNeverReturns(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{ReconnectBackoff: time.Millisecond})
	pubSess := h.client()
	published(t, pubSess, "video")
	sreq, srec := subscribe(t, h.client(), "video", message.Params{})
	subscribed(t, sreq)
	_ = pubSess.Close(message.CodeInternalError, "gone")
	for {
		m := recv(t, srec.messages, "PUBLISH_DONE once the relay stops waiting")
		if pd, ok := m.(*message.PublishDone); ok {
			if pd.Code != message.DoneInternalError {
				t.Errorf("subscription ended with %+v", pd)
			}
			break
		}
	}
}

func TestFetchFromNamespacePublisher(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	o := newOrigin()
	o.store("video", 0, objs("history", "more")...)
	ann, err := h.session(o).Open(context.Background(), &message.PublishNamespace{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, ann).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH_NAMESPACE refused")
	}
	// TRACK_STATUS and FETCH for a track the relay has not seen go to the namespace's publisher (§7.6)
	freq, fr := fetch(t, h.client(), "video", fetchRange(0, 0, 0, 1))
	if _, got := fetched(t, freq, fr); !slices.Equal(got, []string{"history", "more"}) {
		t.Errorf("fetched %v", got)
	}
	if len(o.fetchAsks()) != 1 {
		t.Errorf("one upstream FETCH, got %d", len(o.fetchAsks()))
	}
}

func TestSubscribeTracksParameters(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	pub := published(t, h.client(), "video")
	sendGroup(t, pub, 2, objs("x")...)
	sink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	var p message.Params
	p.SetForward(true)
	p.SetGroupOrder(message.GroupOrderDescending)
	p.SetSubscriberPriority(9)
	p.SetObjectDeliveryTimeout(1000)
	p.SetSubgroupDeliveryTimeout(2000)
	p.SetLocationFilter(message.LocationFilter{Type: message.FilterNextObject})
	p.AuthTokens = []wire.Token{{AliasType: wire.TokenUseValue, Value: []byte("not copied")}}
	viewer := h.session(sink)
	st, err := viewer.Open(context.Background(), &message.SubscribeTracks{Prefix: ns("live"), Params: p}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	strec := newRec()
	st.SetHandlers(strec, nil)
	if _, ok := response(t, st).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	// a track already published is offered at once, with the request's delivery parameters and its largest object
	offered := recv(t, sink.pubs, "the relay's PUBLISH").Message().(*message.Publish)
	op := &offered.Params
	if op.GroupOrder != message.GroupOrderDescending || op.SubscriberPriority != 9 || !op.Forward ||
		op.ObjectDeliveryTimeout != 1000 || op.SubgroupDeliveryTimeout != 2000 || !op.Has(message.ParamLocationFilter) ||
		len(op.AuthTokens) != 0 || !op.Has(message.ParamLargestObject) || op.LargestObject.Group != 2 {
		t.Errorf("offered parameters %+v", op)
	}
	var u message.Params
	u.SetForward(false)
	if _, err = st.Update(u); err != nil {
		t.Fatal(err)
	}
	if _, ok := recv(t, strec.messages, "the update's answer").(*message.RequestOK); !ok {
		t.Error("an update to SUBSCRIBE_TRACKS is accepted")
	}
}

func TestPublisherStateAndDatagrams(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	pub := published(t, h.client(), "video")
	sub := h.client()
	sreq, srec := subscribe(t, sub, "video", message.Params{})
	subscribed(t, sreq)
	if err := pub.SendDatagram(&message.Datagram{Group: 1, Object: 0, Payload: []byte("dg")}); err != nil {
		t.Fatal(err)
	}
	if d := recv(t, srec.datagrams, "a publisher's datagram"); d.payload != "dg" {
		t.Errorf("datagram %+v", d)
	}
	// §9.10: PUBLISH_STATE_NOTIFY is informative; its LARGEST_OBJECT raises the track's
	var n message.Params
	n.SetLargestObject(wire.Location{Group: 7, Object: 3})
	if err := pub.Send(&message.PublishStateNotify{Params: n}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the notified largest object", func() bool {
		req, err := sub.Open(context.Background(), &message.TrackStatus{Track: track("video")}, newRec(), nil)
		if err != nil {
			return false
		}
		ok, isOK := response(t, req).(*message.RequestOK)
		return isOK && ok.Params.LargestObject.Group == 7
	})
}

func TestNamespaceRegistrationsCount(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	announce := func() *session.Request {
		t.Helper()
		r, err := h.client().Open(context.Background(), &message.PublishNamespace{Prefix: ns("live", "a")}, newRec(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := response(t, r).(*message.RequestOK); !ok {
			t.Fatal("PUBLISH_NAMESPACE refused")
		}
		var u message.Params
		u.AuthTokens = []wire.Token{{AliasType: wire.TokenUseValue, Value: []byte("refresh")}}
		if _, err = r.Update(u); err != nil {
			t.Fatal(err)
		}
		return r
	}
	first, second := announce(), announce()
	watched := newRec()
	all, err := h.client().Open(context.Background(), &message.SubscribeNamespace{Prefix: ns("live")}, watched, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, all).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_NAMESPACE refused")
	}
	if n, ok := recv(t, watched.messages, "NAMESPACE").(*message.Namespace); !ok || !n.Suffix.Equal(ns("a")) {
		t.Fatalf("got %+v", n)
	}
	// a namespace two sessions publish is reported once, and withdrawn only when the last of them withdraws it
	first.Cancel(message.ResetCancelled)
	second.Cancel(message.ResetCancelled)
	if d, ok := recv(t, watched.messages, "NAMESPACE_DONE").(*message.NamespaceDone); !ok || !d.Suffix.Equal(ns("a")) {
		t.Fatalf("got %+v", d)
	}
}
