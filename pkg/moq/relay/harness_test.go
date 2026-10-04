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
	"io"
	"math"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/bbolt"
	bboltopts "github.com/trickstercache/trickster/v2/pkg/cache/bbolt/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/memory"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

const wait = 5 * time.Second

func c22(t testing.TB) *codec.Codec {
	t.Helper()
	c, ok := codec.ForDraft(22)
	if !ok {
		t.Fatal("draft 22 missing")
	}
	return c
}

func ns(fields ...string) wire.Namespace {
	out := make(wire.Namespace, len(fields))
	for i, f := range fields {
		out[i] = []byte(f)
	}
	return out
}

func track(name string) message.FullTrackName {
	return message.FullTrackName{Namespace: ns("live"), Name: []byte(name)}
}

func recv[T any](t testing.TB, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wait):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

type pubObj struct {
	id      uint64
	payload []byte
	status  message.ObjectStatus
	props   message.Properties
}

func objs(payloads ...string) []pubObj {
	out := make([]pubObj, len(payloads))
	for i, p := range payloads {
		out[i] = pubObj{id: uint64(i), payload: []byte(p)}
	}
	return out
}

type pubTrack struct {
	groups  map[uint64][]pubObj
	largest wire.Location
	has     bool
	subs    []*session.Request
	forward map[*session.Request]bool
}

type fetchAsk struct {
	start, end wire.Location
	order      message.GroupOrder
}

type origin struct {
	mu         sync.Mutex
	tracks     map[string]*pubTrack
	props      message.Properties
	refuse     map[string]message.RequestCode
	fetches    []fetchAsk
	subscribes atomic.Int32
	statuses   atomic.Int32
	updates    chan *message.RequestUpdate
	ended      chan *session.Request
	datagrams  bool
	hold       chan struct{}
	fetchHold  chan struct{}
	markGaps   bool
}

func newOrigin() *origin {
	return &origin{
		tracks: make(map[string]*pubTrack), refuse: make(map[string]message.RequestCode),
		updates: make(chan *message.RequestUpdate, 64), ended: make(chan *session.Request, 64),
	}
}

func (o *origin) trackLocked(name string) *pubTrack {
	pt := o.tracks[name]
	if pt == nil {
		pt = &pubTrack{groups: make(map[uint64][]pubObj), forward: make(map[*session.Request]bool)}
		o.tracks[name] = pt
	}
	return pt
}

func (o *origin) store(name string, group uint64, os ...pubObj) {
	o.mu.Lock()
	defer o.mu.Unlock()
	pt := o.trackLocked(name)
	pt.groups[group] = append(pt.groups[group], os...)
	for _, ob := range os {
		l := wire.Location{Group: group, Object: ob.id}
		if !pt.has || pt.largest.Less(l) {
			pt.largest, pt.has = l, true
		}
	}
}

func (o *origin) subscriptions(name string) []*session.Request {
	o.mu.Lock()
	defer o.mu.Unlock()
	pt := o.trackLocked(name)
	out := make([]*session.Request, 0, len(pt.subs))
	for _, r := range pt.subs {
		if pt.forward[r] {
			out = append(out, r)
		}
	}
	return out
}

func (o *origin) publish(t testing.TB, name string, group uint64, os ...pubObj) {
	t.Helper()
	o.store(name, group, os...)
	for _, r := range o.subscriptions(name) {
		if o.datagrams {
			for _, ob := range os {
				d := &message.Datagram{Group: group, Object: ob.id, Priority: 1, Payload: ob.payload, Properties: ob.props}
				if ob.status != message.StatusNormal {
					d.HasStatus, d.Status, d.Payload = true, ob.status, nil
				}
				if err := r.SendDatagram(d); err != nil {
					t.Fatal(err)
				}
			}
			continue
		}
		out, err := r.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: group, Priority: 1, EndOfGroup: true},
			3, false)
		if err != nil {
			continue
		}
		for _, ob := range os {
			if writeObject(out, ob) != nil {
				break
			}
		}
		_ = out.Close()
	}
}

func writeObject(out *session.OutgoingSubgroup, ob pubObj) error {
	so := message.SubgroupObject{ID: ob.id, Properties: ob.props, Status: ob.status, PayloadLen: uint64(len(ob.payload))}
	if err := out.WriteObject(&so); err != nil {
		return err
	}
	_, err := out.Write(ob.payload)
	return err
}

func (o *origin) stream(name string, group uint64) []*session.OutgoingSubgroup {
	var outs []*session.OutgoingSubgroup
	for _, r := range o.subscriptions(name) {
		out, err := r.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: group, Priority: 1}, 3, false)
		if err == nil {
			outs = append(outs, out)
		}
	}
	return outs
}

func (o *origin) finish(name string, code message.DoneCode) {
	o.mu.Lock()
	subs := slices.Clone(o.trackLocked(name).subs)
	o.mu.Unlock()
	for _, r := range subs {
		_ = r.Send(&message.PublishDone{Code: code, StreamCount: r.StreamCount(), Reason: "done"})
		_ = r.CloseSend()
	}
}

func (o *origin) fetchAsks() []fetchAsk {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.fetches)
}

func (o *origin) HandleGoAway(*session.Session, *message.GoAway) {}

func (o *origin) HandleRequest(r *session.Request) {
	switch m := r.Message().(type) {
	case *message.Subscribe:
		o.subscribe(r, m)
	case *message.Fetch:
		o.fetch(r, m)
	case *message.TrackStatus:
		o.status(r, m)
	default:
		r.SetHandlers(&originReq{o: o}, nil)
		_ = r.Reject(message.RequestNotSupported, "", 0)
	}
}

func (o *origin) subscribe(r *session.Request, m *message.Subscribe) {
	o.subscribes.Add(1)
	if o.hold != nil {
		<-o.hold
	}
	r.SetHandlers(&originReq{o: o, name: string(m.Track.Name)}, nil)
	name := string(m.Track.Name)
	o.mu.Lock()
	if code, ok := o.refuse[name]; ok {
		o.mu.Unlock()
		_ = r.Reject(code, "refused", 0)
		return
	}
	pt := o.trackLocked(name)
	ok := &message.SubscribeOK{TrackAlias: r.Session().NewTrackAlias(), TrackProperties: o.props}
	if pt.has {
		ok.Params.SetLargestObject(pt.largest)
	}
	pt.subs = append(pt.subs, r)
	pt.forward[r] = m.Params.ForwardOr(true)
	o.mu.Unlock()
	_ = r.Send(ok)
}

func (o *origin) fetch(r *session.Request, m *message.Fetch) {
	r.SetHandlers(&originReq{o: o}, nil)
	if o.fetchHold != nil {
		<-o.fetchHold
	}
	name := string(m.Track.Name)
	o.mu.Lock()
	pt := o.trackLocked(name)
	if !pt.has {
		o.mu.Unlock()
		_ = r.Reject(message.RequestInvalidRange, "empty", 0)
		return
	}
	largest := pt.largest
	start, end, _, _ := m.Params.LocationFilter.Bounds(&largest, true)
	if largest.Less(end) {
		end = largest
	}
	order := message.GroupOrderAscending
	if m.Params.Has(message.ParamGroupOrder) {
		order = m.Params.GroupOrder
	}
	o.fetches = append(o.fetches, fetchAsk{start: start, end: end, order: order})
	var groups []uint64
	for g := range pt.groups {
		if g >= start.Group && g <= end.Group {
			groups = append(groups, g)
		}
	}
	slices.Sort(groups)
	if order == message.GroupOrderDescending {
		slices.Reverse(groups)
	}
	type entry struct {
		g      uint64
		ob     pubObj
		marker bool
	}
	var entries []entry
	if o.markGaps && len(groups) > 0 && groups[0] > start.Group && order == message.GroupOrderAscending {
		entries = append(entries, entry{g: groups[0] - 1, ob: pubObj{id: math.MaxUint64}, marker: true})
	}
	for _, g := range groups {
		for _, ob := range pt.groups[g] {
			l := wire.Location{Group: g, Object: ob.id}
			if !l.Less(start) && !end.Less(l) && ob.status == message.StatusNormal {
				entries = append(entries, entry{g: g, ob: ob})
			}
		}
	}
	o.mu.Unlock()
	if r.Send(&message.FetchOK{EndLocation: end}) != nil {
		return
	}
	out, err := r.OpenFetch(context.Background(), r.RequestID(), order, 3)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.marker {
			if out.WriteObject(&message.FetchObject{EndOfRange: message.RangeEndNonExistent, Group: e.g, Object: e.ob.id}) != nil {
				return
			}
			continue
		}
		fo := message.FetchObject{Group: e.g, Object: e.ob.id, Priority: 1, Properties: e.ob.props, PayloadLen: uint64(len(e.ob.payload))}
		if out.WriteObject(&fo) != nil {
			return
		}
		if _, err = out.Write(e.ob.payload); err != nil {
			return
		}
	}
	_ = out.Close()
	_ = r.CloseSend()
}

func (o *origin) status(r *session.Request, m *message.TrackStatus) {
	o.statuses.Add(1)
	r.SetHandlers(&originReq{o: o}, nil)
	o.mu.Lock()
	pt, known := o.tracks[string(m.Track.Name)]
	o.mu.Unlock()
	if !known || !pt.has {
		_ = r.Reject(message.RequestDoesNotExist, "no such track", 0)
		return
	}
	ok := &message.RequestOK{TrackProperties: o.props}
	ok.Params.SetLargestObject(pt.largest)
	if r.Send(ok) == nil {
		_ = r.CloseSend()
	}
}

type originReq struct {
	o    *origin
	name string
}

func (h *originReq) HandleMessage(r *session.Request, m message.Message) {
	u, ok := m.(*message.RequestUpdate)
	if !ok {
		return
	}
	if u.Params.Has(message.ParamForward) {
		h.o.mu.Lock()
		h.o.trackLocked(h.name).forward[r] = u.Params.Forward
		h.o.mu.Unlock()
	}
	h.o.updates <- u
	_ = r.Send(&message.RequestOK{})
}

func (h *originReq) HandleClose(r *session.Request, _ error) {
	if h.name == "" {
		return
	}
	h.o.mu.Lock()
	pt := h.o.trackLocked(h.name)
	if i := slices.Index(pt.subs, r); i >= 0 {
		pt.subs = slices.Delete(pt.subs, i, i+1)
		delete(pt.forward, r)
	}
	h.o.mu.Unlock()
	h.o.ended <- r
}

type upstream struct {
	o        *origin
	mu       sync.Mutex
	sess     *session.Session
	sessions []*session.Session
	dials    atomic.Int32
	down     atomic.Bool
}

func (u *upstream) Session(ctx context.Context, _ relay.Scope) (*session.Session, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.down.Load() {
		return nil, errors.New("origin down")
	}
	if u.sess != nil && u.sess.Err() == nil {
		return u.sess, nil
	}
	cc, sc := memconn.Pair()
	codec, _ := codec.ForDraft(22)
	server, _ := session.New(sc, session.Options{Role: session.RoleServer, Codec: codec, Handler: u.o})
	client, _ := session.New(cc, session.Options{
		Role: session.RoleClient, Codec: codec,
		Setup: message.Setup{Implementation: "relay"},
	})
	errs := make(chan error, 2)
	go func() { errs <- server.Start(ctx) }()
	go func() { errs <- client.Start(ctx) }()
	for range 2 {
		if err := <-errs; err != nil {
			return nil, err
		}
	}
	u.sess = client
	u.dials.Add(1)
	u.sessions = append(u.sessions, client, server)
	return client, nil
}

func (u *upstream) close() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, s := range u.sessions {
		_ = s.Close(message.CodeNoError, "")
	}
}

func (u *upstream) kill() {
	u.mu.Lock()
	s := u.sess
	u.mu.Unlock()
	if s != nil {
		_ = s.Close(message.CodeGoAwayTimeout, "killed")
	}
}

type countingCache struct {
	cache.Cache
	stores atomic.Int32
}

func (c *countingCache) Store(key string, data []byte, ttl time.Duration) error {
	defer c.stores.Add(1)
	return c.Cache.Store(key, data, ttl)
}

type memoryCache struct {
	*memory.Cache
	stores *atomic.Int32
}

func (c memoryCache) Configuration() *options.Options {
	o := options.New()
	o.Provider = "memory"
	return o
}

func (c memoryCache) StoreReference(key string, data cache.ReferenceObject, ttl time.Duration) error {
	defer c.stores.Add(1)
	return c.Cache.StoreReference(key, data, ttl)
}

func newMemoryCache() (cache.Cache, *atomic.Int32) {
	var n atomic.Int32
	return memoryCache{Cache: memory.New("test", nil), stores: &n}, &n
}

type boltCache struct {
	*bbolt.CacheClient
}

func (c boltCache) Configuration() *options.Options {
	return c.Config
}

func newBoltCache(t *testing.T) (cache.Cache, *atomic.Int32) {
	t.Helper()
	cfg := options.New()
	cfg.Provider = "bbolt"
	cfg.BBolt = &bboltopts.Options{Filename: filepath.Join(t.TempDir(), "moq.db"), Bucket: "moq"}
	c := bbolt.New("moq", "", "", cfg)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	cc := &countingCache{Cache: boltCache{c}}
	return cc, &cc.stores
}

type cacheProvider struct {
	c cache.Cache
}

func (p cacheProvider) Cache() cache.Cache {
	return p.c
}

type harness struct {
	t     testing.TB
	o     *origin
	up    *upstream
	relay *relay.Relay
	scope relay.Scope
}

func newHarness(t testing.TB, opts relay.Options, c cache.Cache) *harness {
	t.Helper()
	o := newOrigin()
	h := &harness{t: t, o: o, up: &upstream{o: o}, scope: relay.Scope{Authority: "relay.test", Path: "/"}}
	var cp relay.CacheProvider
	if c != nil {
		cp = cacheProvider{c}
	}
	h.relay = relay.New(opts, h.up, cp)
	t.Cleanup(func() {
		h.relay.Close()
		h.up.close()
	})
	return h
}

func (h *harness) client() *session.Session {
	h.t.Helper()
	return h.clientWith(nil)
}

func (h *harness) clientWith(wrap func(*memconn.Conn) session.Conn) *session.Session {
	h.t.Helper()
	return h.connect(wrap, nil)
}

func (h *harness) connect(wrap, wrapServer func(*memconn.Conn) session.Conn) *session.Session {
	h.t.Helper()
	cc, sc := memconn.Pair()
	var conn, sconn session.Conn = cc, sc
	if wrap != nil {
		conn = wrap(cc)
	}
	if wrapServer != nil {
		sconn = wrapServer(sc)
	}
	server, err := session.New(sconn, session.Options{
		Role: session.RoleServer, Codec: c22(h.t), Handler: h.relay.Handler(h.scope),
		Setup: message.Setup{MaxRequestUpdates: 16},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	client, err := session.New(conn, session.Options{
		Role: session.RoleClient, Codec: c22(h.t),
		Setup: message.Setup{Path: h.scope.Path, HasPath: true, Authority: h.scope.Authority, HasAuthority: true},
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

type got struct {
	group, id uint64
	payload   string
}

type rec struct {
	mu        sync.Mutex
	objects   []got
	streams   int
	resets    int
	fetched   []message.FetchObject
	payloads  []string
	messages  chan message.Message
	closes    chan error
	fetchDone chan struct{}
	datagrams chan got
}

func newRec() *rec {
	return &rec{
		messages: make(chan message.Message, 64), closes: make(chan error, 4),
		fetchDone: make(chan struct{}, 8), datagrams: make(chan got, 256),
	}
}

func (r *rec) HandleMessage(_ *session.Request, m message.Message) { r.messages <- m }
func (r *rec) HandleClose(_ *session.Request, err error)           { r.closes <- err }

func (r *rec) HandleDatagram(_ *session.Request, d *message.Datagram) {
	r.datagrams <- got{group: d.Group, id: d.Object, payload: string(d.Payload)}
}

func (r *rec) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	r.mu.Lock()
	r.streams++
	r.mu.Unlock()
	g := in.Header().Group
	for {
		o, err := in.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				r.mu.Lock()
				r.resets++
				r.mu.Unlock()
			}
			return
		}
		b, err := io.ReadAll(in)
		if err != nil {
			r.mu.Lock()
			r.resets++
			r.mu.Unlock()
			return
		}
		if o.Status != message.StatusNormal {
			continue
		}
		r.mu.Lock()
		r.objects = append(r.objects, got{group: g, id: o.ID, payload: string(b)})
		r.mu.Unlock()
	}
}

func (r *rec) HandleFetch(_ *session.Request, in *session.IncomingFetch) {
	defer func() { r.fetchDone <- struct{}{} }()
	for {
		fo, err := in.Next()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(in)
		r.mu.Lock()
		r.fetched = append(r.fetched, fo)
		r.payloads = append(r.payloads, string(b))
		r.mu.Unlock()
	}
}

func (r *rec) received() []got {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.objects)
	slices.SortFunc(out, func(a, b got) int {
		switch {
		case a.group != b.group:
			return int(a.group) - int(b.group)
		default:
			return int(a.id) - int(b.id)
		}
	})
	return out
}

func (r *rec) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.objects)
}

func subscribe(t testing.TB, c *session.Session, name string, p message.Params) (*session.Request, *rec) {
	t.Helper()
	r := newRec()
	req, err := c.Open(context.Background(), &message.Subscribe{Track: track(name), Params: p}, r, r)
	if err != nil {
		t.Fatal(err)
	}
	return req, r
}

func subscribed(t testing.TB, req *session.Request) *message.SubscribeOK {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	m, err := req.Response(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := m.(*message.SubscribeOK)
	if !isOK {
		t.Fatalf("subscription refused: %+v", m)
	}
	return ok
}

func fetch(t testing.TB, c *session.Session, name string, p message.Params) (*session.Request, *rec) {
	t.Helper()
	r := newRec()
	req, err := c.Open(context.Background(), &message.Fetch{Track: track(name), Params: p}, r, r)
	if err != nil {
		t.Fatal(err)
	}
	return req, r
}

func fetched(t testing.TB, req *session.Request, r *rec) (*message.FetchOK, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	m, err := req.Response(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := m.(*message.FetchOK)
	if !isOK {
		t.Fatalf("fetch refused: %+v", m)
	}
	recv(t, r.fetchDone, "the fetch stream")
	r.mu.Lock()
	defer r.mu.Unlock()
	return ok, slices.Clone(r.payloads)
}

func fetchRange(sg, so, eg, eo uint64) message.Params {
	var p message.Params
	p.SetLocationFilter(message.LocationFilter{
		Type: message.FilterAbsoluteRange, StartGroup: sg, StartObject: so,
		EndGroupDelta: eg - sg, EndObject: eo,
	})
	return p
}

type prioConn struct {
	*memconn.Conn
	mu    sync.Mutex
	prios []prio
}

type prio struct {
	urgency     int8
	incremental bool
}

func (c *prioConn) AcceptUniStream(ctx context.Context) (session.ReceiveStream, error) {
	rs, err := c.Conn.AcceptUniStream(ctx)
	if err == nil {
		u, incr, _ := rs.(*memconn.ReceiveStream).Priority()
		c.mu.Lock()
		c.prios = append(c.prios, prio{u, incr})
		c.mu.Unlock()
	}
	return rs, err
}

func (c *prioConn) seen() []prio {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.prios)
}

func waitLargest(t testing.TB, c *session.Session, name string, group, object uint64) {
	t.Helper()
	want := wire.Location{Group: group, Object: object}
	eventually(t, "the relay to see the object", func() bool {
		req, err := c.Open(context.Background(), &message.TrackStatus{Track: track(name)}, newRec(), nil)
		if err != nil {
			return false
		}
		m, err := req.Response(context.Background())
		ok, isOK := m.(*message.RequestOK)
		return err == nil && isOK && ok.Params.Has(message.ParamLargestObject) && !ok.Params.LargestObject.Less(want)
	})
}
