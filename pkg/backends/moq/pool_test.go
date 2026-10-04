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

package moq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/hrw"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

type poolFixture struct {
	clients map[string]*Client
	status  map[string]*healthcheck.Status
	pool    pool.Pool
	picker  *lb.Balancer
}

func newPoolFixture(t *testing.T, origins map[string]string) *poolFixture {
	t.Helper()
	f := &poolFixture{clients: map[string]*Client{}, status: map[string]*healthcheck.Status{}}
	var targets pool.Targets
	for name, addr := range origins {
		f.clients[name] = newBackend(t, name, "moqt://"+addr+"/")
		f.status[name] = healthcheck.NewStatus(name, "", "", healthcheck.StatusPassing, time.Time{}, nil)
		targets = append(targets, pool.NewTarget(nil, f.status[name], f.clients[name]))
	}
	f.pool = pool.New(targets, 1)
	t.Cleanup(f.pool.Stop)
	f.picker = lb.NewBalancer(hrw.New(), lb.BalancerOptions{Pool: f.pool.Core()})
	return f
}

func (f *poolFixture) memberOf(s *session.Session) string {
	for name, c := range f.clients {
		c.sh.dialer.mu.Lock()
		current := c.sh.dialer.sess
		c.sh.dialer.mu.Unlock()
		if current == s {
			return name
		}
	}
	return ""
}

func (f *poolFixture) other(name string) string {
	for n := range f.clients {
		if n != name {
			return n
		}
	}
	return ""
}

func poolTrack(name string) message.FullTrackName {
	return message.FullTrackName{Namespace: wire.Namespace{[]byte("live")}, Name: []byte(name)}
}

func TestPoolUpstream(t *testing.T) {
	f := newPoolFixture(t, map[string]string{"a": startOrigin(t).addr, "b": startOrigin(t).addr})
	up := newPoolUpstream("origins")
	ctx := context.Background()
	video := poolTrack("video")
	if _, _, err := up.Session(ctx, relay.Scope{}, video); !errors.Is(err, errNoMember) {
		t.Fatalf("an alb bound by no configuration offers no origin: %v", err)
	}
	up.bind(f.picker)
	pick := func(what string) (string, func(error)) {
		t.Helper()
		s, release, err := up.Session(ctx, relay.Scope{}, video)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return f.memberOf(s), release
	}

	// a track stays on the member the strategy places it on
	first, release := pick("the first subscription")
	again, releaseAgain := pick("a second subscription")
	if first == "" || again != first {
		t.Fatalf("the track moved from %q to %q", first, again)
	}
	releaseAgain(nil)

	// a failure there sends the retry to the other member, and a success there lets the track return
	release(errors.New("origin lost"))
	retried, releaseRetry := pick("the retry")
	if retried != f.other(first) {
		t.Fatalf("the retry went to %q, not away from %q", retried, first)
	}
	releaseRetry(nil)
	back, releaseBack := pick("after the retry succeeded")
	if back != first {
		t.Errorf("the track returns to %q once forgiven, but went to %q", first, back)
	}
	releaseBack(context.Canceled)

	// a member that fails its health check is passed over until it recovers
	f.status[first].Set(healthcheck.StatusFailing)
	if during, r := pick("while unhealthy"); during != f.other(first) {
		t.Errorf("an unhealthy member got the track")
	} else {
		r(nil)
	}
	f.status[first].Set(healthcheck.StatusPassing)
	if after, r := pick("after recovery"); after != first {
		t.Errorf("the recovered member did not get the track back, %q did", after)
	} else {
		r(nil)
	}
}

func TestPoolUpstreamDialFailure(t *testing.T) {
	// nothing answers on a port taken from a closed listener
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.LocalAddr().String()
	_ = ln.Close()
	f := newPoolFixture(t, map[string]string{"dead": dead, "live": startOrigin(t).addr})
	up := newPoolUpstream("origins")
	up.bind(f.picker)
	// find a track the strategy places on the dead member
	var track message.FullTrackName
	for i := range 64 {
		track = poolTrack(fmt.Sprintf("track-%d", i))
		key := trackKey(track)
		pk, ok := lb.PickLeaf(f.picker, lb.Flow{Key: key, HasKey: true})
		if !ok {
			t.Fatal("no member to pick")
		}
		name := memberClient(pk.Member()).Name()
		pk.Done(lb.OutcomeCanceled)
		if name == "dead" {
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, _, err := up.Session(ctx, relay.Scope{}, track); err == nil {
		t.Fatal("a member whose origin does not answer offers no session")
	}
	s, release, err := up.Session(context.Background(), relay.Scope{}, track)
	if err != nil {
		t.Fatal(err)
	}
	defer release(nil)
	if got := f.memberOf(s); got != "live" {
		t.Errorf("the retry after a failed dial went to %q", got)
	}
}

func TestPoolBoundAtCommit(t *testing.T) {
	o := startOrigin(t)
	member := newBackend(t, "origin-a", "moqt://"+o.addr+"/")
	edge := newBackendWith(t, "edge", "", func(o *bo.Options) { o.MoQ.Upstream.ALB = "origins" })
	if edge.sh.pool == nil || edge.sh.dialer != nil {
		t.Fatal("a backend naming an alb reaches its origins through it")
	}
	if msg := edge.HealthCheckUnsupported(); msg == "" {
		t.Error("a backend reaching its origins through an alb is not probed itself")
	}
	p := pool.New(pool.Targets{pool.NewTarget(nil, healthcheck.NewStatus("origin-a", "", "",
		healthcheck.StatusPassing, time.Time{}, nil), member)}, 1)
	t.Cleanup(p.Stop)
	alb := &pickerBackend{Backend: member.Backend, picker: lb.NewBalancer(hrw.New(),
		lb.BalancerOptions{Pool: p.Core()})}
	RetireClients(backends.Backends{"edge": edge, "origin-a": member, "origins": alb})
	if _, release, err := edge.sh.pool.Session(context.Background(), relay.Scope{}, poolTrack("video")); err != nil {
		t.Fatalf("the committed alb offers its member: %v", err)
	} else {
		release(nil)
	}
	// a configuration whose alb is gone leaves the relay with no origin until one commits again
	RetireClients(backends.Backends{"edge": edge, "origin-a": member})
	if _, _, err := edge.sh.pool.Session(context.Background(), relay.Scope{}, poolTrack("video")); !errors.Is(err,
		errNoMember) {
		t.Errorf("an alb missing from the configuration still offered an origin: %v", err)
	}
}

type pickerBackend struct {
	backends.Backend
	picker lb.Picker
}

func (p *pickerBackend) Picker() lb.Picker {
	return p.picker
}
