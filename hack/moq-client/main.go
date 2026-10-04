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

// Command moq-client subscribes to, publishes to or fetches from a MoQ Transport relay over native QUIC (moqt://) or
// WebTransport (https://); it publishes synthetic objects whose integrity a subscriber verifies byte for byte.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	payloadMagic   = "TMOQ"
	headerSize     = 24 // the magic, the group and object IDs, and a CRC-32 of the random bytes after them
	implementation = "trickster-moq-client"
	schemeMoQT     = "moqt"
	schemeHTTPS    = "https"
	defaultPort    = "443"
	rootPath       = "/"
	trackSeparator = "/"
	keepAlive      = 15 * time.Second
	urgency        = 3 // a mid urgency for published subgroup streams
	transportQUIC  = "quic"
	transportWT    = "webtransport"

	verified   = "verified"
	unverified = "unverified"
	corrupt    = "CORRUPT"
)

type options struct {
	url, sub, pub, fetch, ca, token string
	insecure                        bool
	draft                           int
	groups, objects, size, keySize  int
	from, to                        uint64
	interval, timeout               time.Duration
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var o options
	flag.StringVar(&o.url, "url", "", "the relay: moqt://host[:port][/path] for native QUIC, or "+
		"https://host[:port]/path for WebTransport (required)")
	flag.StringVar(&o.sub, "sub", "", "subscribe to a track, written as its namespace fields and name joined "+
		"by '/', such as live/video")
	flag.StringVar(&o.pub, "pub", "", "publish synthetic groups to a track, such as live/video")
	flag.StringVar(&o.fetch, "fetch", "", "fetch the groups -from through -to of a track")
	flag.Uint64Var(&o.from, "from", 0, "the first group -fetch asks for")
	flag.Uint64Var(&o.to, "to", 0, "the last group -fetch asks for")
	flag.IntVar(&o.groups, "groups", 0, "groups to publish, or to receive before exiting; 0 runs until interrupted")
	flag.IntVar(&o.objects, "objects", 3, "objects after the first in each published group")
	flag.IntVar(&o.keySize, "key-size", 16384, "payload bytes of each published group's first object")
	flag.IntVar(&o.size, "size", 1024, "payload bytes of each published group's other objects")
	flag.DurationVar(&o.interval, "interval", time.Second, "the time between published groups")
	flag.IntVar(&o.draft, "draft", 0, "offer only this draft; 0 offers every supported draft")
	flag.StringVar(&o.ca, "ca", "", "path to a PEM CA bundle trusted for the relay's certificate")
	flag.BoolVar(&o.insecure, "insecure", false, "skip certificate verification (development only)")
	flag.StringVar(&o.token, "token", "", "an AUTHORIZATION_TOKEN sent in SETUP")
	flag.DurationVar(&o.timeout, "timeout", 0, "exit after this long; 0 runs until done or interrupted")
	flag.Parse()

	modes := 0
	for _, m := range []string{o.sub, o.pub, o.fetch} {
		if m != "" {
			modes++
		}
	}
	if o.url == "" || modes != 1 {
		flag.Usage()
		return errors.New("-url and exactly one of -sub, -pub or -fetch are required")
	}
	if o.keySize < headerSize || o.size < headerSize {
		return fmt.Errorf("-key-size and -size must be at least %d bytes", headerSize)
	}
	if o.groups < 0 || o.objects < 0 {
		return errors.New("-groups and -objects must not be negative")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if o.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	switch {
	case o.sub != "":
		return subscribe(ctx, o)
	case o.fetch != "":
		return fetch(ctx, o)
	}
	return publish(ctx, o)
}

func parseTrack(s string) (message.FullTrackName, error) {
	fields := strings.Split(strings.Trim(s, trackSeparator), trackSeparator)
	if len(fields) < 2 || slices.Contains(fields, "") {
		return message.FullTrackName{}, fmt.Errorf("track %q needs at least one namespace field and a name", s)
	}
	ns := make(wire.Namespace, 0, len(fields)-1)
	for _, f := range fields[:len(fields)-1] {
		ns = append(ns, []byte(f))
	}
	return message.FullTrackName{Namespace: ns, Name: []byte(fields[len(fields)-1])}, nil
}

func tlsConfig(o options) (*tls.Config, error) {
	tc := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: o.insecure, // #nosec G402 -- development tool, opt-in via flag
	}
	if o.ca == "" {
		return tc, nil
	}
	pem, err := os.ReadFile(o.ca)
	if err != nil {
		return nil, fmt.Errorf("reading CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in %s", o.ca)
	}
	tc.RootCAs = pool
	return tc, nil
}

func dial(ctx context.Context, o options, h session.Handler) (*session.Session, func(), error) {
	u, err := url.Parse(o.url)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing -url: %w", err)
	}
	drafts := codec.ClientOffer
	if o.draft != 0 {
		if _, ok := codec.ForDraft(o.draft); !ok {
			return nil, nil, fmt.Errorf("draft %d is not supported; supported drafts are %v", o.draft, codec.Drafts())
		}
		drafts = []int{o.draft}
	}
	tc, err := tlsConfig(o)
	if err != nil {
		return nil, nil, err
	}
	tc.ServerName = u.Hostname()
	qc := &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true, KeepAlivePeriod: keepAlive}
	setup := message.Setup{Implementation: implementation}
	if o.token != "" {
		setup.AuthTokens = []wire.Token{{AliasType: wire.TokenUseValue, Value: []byte(o.token)}}
	}
	var (
		conn      session.Conn
		c         *codec.Codec
		ok        bool
		transport string
		closeFn   = func() {}
	)
	switch strings.ToLower(u.Scheme) {
	case schemeMoQT:
		port := u.Port()
		if port == "" {
			port = defaultPort
		}
		tc.NextProtos = codec.ALPNs(drafts)
		qconn, err := quic.DialAddr(ctx, net.JoinHostPort(u.Hostname(), port), tc, qc)
		if err != nil {
			return nil, nil, err
		}
		if c, ok = codec.ForALPN(qconn.ConnectionState().TLS.NegotiatedProtocol); !ok {
			_ = qconn.CloseWithError(quic.ApplicationErrorCode(message.CodeVersionNegotiation), "")
			return nil, nil, errors.New("the relay negotiated no supported draft")
		}
		path := u.Path
		if path == "" {
			path = rootPath
		}
		setup.Path, setup.HasPath, setup.Authority, setup.HasAuthority = path, true, u.Host, true
		conn, transport = session.FromQUIC(qconn), transportQUIC
	case schemeHTTPS:
		tc.NextProtos = []string{http3.NextProtoH3}
		tr := &webtransport.Transport{
			TLSClientConfig: tc, QUICConfig: qc, ApplicationProtocols: codec.ALPNs(drafts), DialAddr: quic.DialAddr,
		}
		closeFn = func() { _ = tr.Close() }
		_, ws, err := tr.Dial(ctx, u.String(), nil)
		if err != nil {
			closeFn()
			return nil, nil, err
		}
		if c, ok = codec.ForALPN(ws.SessionState().ApplicationProtocol); !ok {
			_ = ws.CloseWithError(webtransport.WTALPNErrorCode, "")
			closeFn()
			return nil, nil, errors.New("the relay negotiated no supported draft")
		}
		conn, transport = session.FromWebTransport(ws), transportWT
	default:
		return nil, nil, fmt.Errorf("-url must use the %s:// or %s:// scheme", schemeMoQT, schemeHTTPS)
	}
	s, err := session.New(conn, session.Options{
		Role: session.RoleClient, Codec: c, Setup: setup, Handler: h, WebTransport: transport == transportWT,
	})
	if err != nil {
		_ = conn.CloseWithError(uint64(message.CodeInternalError), "")
		closeFn()
		return nil, nil, err
	}
	if err = s.Start(ctx); err != nil {
		closeFn()
		return nil, nil, err
	}
	fmt.Fprintf(os.Stderr, "connected to %s: draft %d over %s\n", u.Host, c.Draft(), transport)
	return s, func() {
		_ = s.Close(message.CodeNoError, "")
		closeFn()
	}, nil
}

func answer(ctx context.Context, req *session.Request, want message.Kind) error {
	m, err := req.Response(ctx)
	if err != nil {
		return err
	}
	if re, refused := m.(*message.RequestError); refused {
		return fmt.Errorf("refused: %s (code %#x)", re.Reason, uint64(re.Code))
	}
	if m.Kind() != want {
		return fmt.Errorf("unexpected answer %s", m.Kind())
	}
	return nil
}

func subscribe(ctx context.Context, o options) error {
	ft, err := parseTrack(o.sub)
	if err != nil {
		return err
	}
	s, closeFn, err := dial(ctx, o, nil)
	if err != nil {
		return err
	}
	defer closeFn()
	r := newReceiver(o.groups)
	req, err := s.Open(ctx, &message.Subscribe{Track: ft}, r, r)
	if err != nil {
		return err
	}
	if err = answer(ctx, req, message.KindSubscribeOK); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "subscribed to %s\n", o.sub)
	return r.wait(ctx, s)
}

func fetch(ctx context.Context, o options) error {
	ft, err := parseTrack(o.fetch)
	if err != nil {
		return err
	}
	if o.to < o.from {
		return errors.New("-to must not be before -from")
	}
	s, closeFn, err := dial(ctx, o, nil)
	if err != nil {
		return err
	}
	defer closeFn()
	r := newReceiver(0)
	r.fetch = true
	var p message.Params
	p.SetLocationFilter(message.LocationFilter{
		Type: message.FilterAbsoluteStartGroupEnd, StartGroup: o.from, EndGroupDelta: o.to - o.from,
	})
	req, err := s.Open(ctx, &message.Fetch{Track: ft, Params: p}, r, r)
	if err != nil {
		return err
	}
	if err = answer(ctx, req, message.KindFetchOK); err != nil {
		return err
	}
	return r.wait(ctx, s)
}

type receiver struct {
	want  int
	fetch bool
	done  chan struct{}
	once  sync.Once

	mu                       sync.Mutex
	groups, objects, corrupt int
	bytes                    int64
	err                      error
}

func newReceiver(groups int) *receiver {
	return &receiver{want: groups, done: make(chan struct{})}
}

func (r *receiver) finish(err error) {
	r.once.Do(func() {
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
		close(r.done)
	})
}

func (r *receiver) wait(ctx context.Context, s *session.Session) error {
	var err error
	select {
	case <-r.done:
	case <-ctx.Done():
	case <-s.Done():
		err = s.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(os.Stderr, "received %d objects (%d bytes) in %d groups; %d corrupt\n", r.objects, r.bytes, r.groups,
		r.corrupt)
	if err == nil {
		err = r.err
	}
	if err == nil && r.corrupt > 0 {
		err = fmt.Errorf("%d objects failed verification", r.corrupt)
	}
	return err
}

func (r *receiver) object(group, object uint64, payload []byte) {
	status := verify(group, object, payload)
	r.mu.Lock()
	r.objects++
	r.bytes += int64(len(payload))
	if status == corrupt {
		r.corrupt++
	}
	r.mu.Unlock()
	fmt.Printf("group=%d object=%d bytes=%d %s\n", group, object, len(payload), status)
}

func (r *receiver) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	group := in.Header().Group
	for {
		obj, err := in.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.groupDone()
			}
			return
		}
		payload, err := io.ReadAll(in)
		if err != nil {
			return
		}
		r.object(group, obj.ID, payload)
	}
}

func (r *receiver) groupDone() {
	r.mu.Lock()
	r.groups++
	reached := r.want > 0 && r.groups >= r.want
	r.mu.Unlock()
	if reached {
		r.finish(nil)
	}
}

func (r *receiver) HandleDatagram(_ *session.Request, d *message.Datagram) {
	r.object(d.Group, d.Object, d.Payload)
}

func (r *receiver) HandleFetch(_ *session.Request, in *session.IncomingFetch) {
	defer r.finish(nil)
	seen := map[uint64]bool{}
	for {
		obj, err := in.Next()
		if err != nil {
			return
		}
		if obj.EndOfRange != message.RangeEndNone {
			fmt.Printf("end of range at group=%d object=%d (kind %d)\n", obj.Group, obj.Object, obj.EndOfRange)
			continue
		}
		payload, err := io.ReadAll(in)
		if err != nil {
			return
		}
		r.object(obj.Group, obj.Object, payload)
		if !seen[obj.Group] {
			seen[obj.Group] = true
			r.mu.Lock()
			r.groups++
			r.mu.Unlock()
		}
	}
}

func (r *receiver) HandleMessage(_ *session.Request, m message.Message) {
	if done, ok := m.(*message.PublishDone); ok {
		fmt.Fprintf(os.Stderr, "PUBLISH_DONE: %s (code %#x)\n", done.Reason, uint64(done.Code))
	}
}

func (r *receiver) HandleClose(_ *session.Request, err error) {
	// a fetch's objects arrive on their own stream, which can outlast the request that asked for them
	if r.fetch && err == nil {
		return
	}
	r.finish(err)
}

func verify(group, object uint64, payload []byte) string {
	if len(payload) < headerSize || string(payload[:len(payloadMagic)]) != payloadMagic {
		return unverified
	}
	g := binary.BigEndian.Uint64(payload[4:12])
	id := binary.BigEndian.Uint64(payload[12:20])
	sum := binary.BigEndian.Uint32(payload[20:headerSize])
	if g != group || id != object || crc32.ChecksumIEEE(payload[headerSize:]) != sum {
		return corrupt
	}
	return verified
}

func synthetic(group, object uint64, size int) ([]byte, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b[headerSize:]); err != nil {
		return nil, err
	}
	copy(b, payloadMagic)
	binary.BigEndian.PutUint64(b[4:12], group)
	binary.BigEndian.PutUint64(b[12:20], object)
	binary.BigEndian.PutUint32(b[20:headerSize], crc32.ChecksumIEEE(b[headerSize:]))
	return b, nil
}

type publisher struct {
	track message.FullTrackName

	mu   sync.Mutex
	subs map[*session.Request]uint64
}

func (p *publisher) HandleGoAway(*session.Session, *message.GoAway) {}

func (p *publisher) HandleRequest(r *session.Request) {
	r.SetHandlers(p, nil)
	sub, ok := r.Message().(*message.Subscribe)
	if !ok {
		// a relay may also ask about namespaces, and carries on without them when refused
		_ = r.Reject(message.RequestNotSupported, "only SUBSCRIBE is served", 0)
		return
	}
	if !sub.Track.Equal(p.track) {
		_ = r.Reject(message.RequestDoesNotExist, "not published here", 0)
		return
	}
	if r.Send(&message.SubscribeOK{TrackAlias: r.Session().NewTrackAlias()}) != nil {
		return
	}
	p.mu.Lock()
	p.subs[r] = 0
	n := len(p.subs)
	p.mu.Unlock()
	fmt.Fprintf(os.Stderr, "subscribed by the relay (%d subscriptions)\n", n)
}

func (p *publisher) HandleMessage(*session.Request, message.Message) {}

func (p *publisher) HandleClose(r *session.Request, _ error) {
	p.mu.Lock()
	delete(p.subs, r)
	p.mu.Unlock()
}

func (p *publisher) group(ctx context.Context, o options, group uint64) (int, error) {
	payloads := make([][]byte, 0, o.objects+1)
	for i := range o.objects + 1 {
		size := o.size
		if i == 0 {
			size = o.keySize
		}
		b, err := synthetic(group, uint64(i), size)
		if err != nil {
			return 0, err
		}
		payloads = append(payloads, b)
	}
	p.mu.Lock()
	subs := make([]*session.Request, 0, len(p.subs))
	for r := range p.subs {
		subs = append(subs, r)
		p.subs[r]++
	}
	p.mu.Unlock()
	for _, r := range subs {
		if err := send(ctx, r, group, payloads); err != nil {
			fmt.Fprintf(os.Stderr, "group %d not sent to a subscription: %v\n", group, err)
		}
	}
	return len(subs), nil
}

func send(ctx context.Context, r *session.Request, group uint64, payloads [][]byte) error {
	out, err := r.OpenSubgroup(ctx, message.SubgroupHeader{
		Group: group, Priority: 1, EndOfGroup: true, FirstObject: true,
	}, urgency, false)
	if err != nil {
		return err
	}
	for i, b := range payloads {
		if err = out.WriteObject(&message.SubgroupObject{ID: uint64(i), PayloadLen: uint64(len(b))}); err != nil {
			return err
		}
		if _, err = out.Write(b); err != nil {
			return err
		}
	}
	return out.Close()
}

func (p *publisher) end() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for r, streams := range p.subs {
		if r.Send(&message.PublishDone{Code: message.DoneTrackEnded, StreamCount: streams, Reason: "done"}) == nil {
			_ = r.CloseSend()
		}
	}
}

func publish(ctx context.Context, o options) error {
	ft, err := parseTrack(o.pub)
	if err != nil {
		return err
	}
	p := &publisher{track: ft, subs: make(map[*session.Request]uint64)}
	s, closeFn, err := dial(ctx, o, p)
	if err != nil {
		return err
	}
	defer closeFn()
	req, err := s.Open(ctx, &message.PublishNamespace{Prefix: ft.Namespace}, p, nil)
	if err != nil {
		return err
	}
	if err = answer(ctx, req, message.KindRequestOK); err != nil {
		return err
	}
	// a group ID from the clock never repeats one an earlier run cached under the same track
	first := uint64(time.Now().Unix()) // #nosec G115 -- the clock is past the epoch
	last := first + uint64(o.groups)   // #nosec G115 -- checked non-negative in run
	fmt.Fprintf(os.Stderr, "publishing %s from group %d\n", o.pub, first)
	tick := time.NewTicker(o.interval)
	defer tick.Stop()
	for g := first; o.groups == 0 || g < last; g++ {
		n, err := p.group(ctx, o, g)
		if err != nil {
			return err
		}
		fmt.Printf("group=%d objects=%d subscriptions=%d\n", g, o.objects+1, n)
		select {
		case <-ctx.Done():
			p.end()
			return nil
		case <-s.Done():
			return s.Err()
		case <-tick.C:
		}
	}
	p.end()
	return nil
}
