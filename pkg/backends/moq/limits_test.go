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
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"

	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit"
	rlopts "github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func rateLimiter(t *testing.T, limit int64, keys ...string) *rlopts.Options {
	t.Helper()
	// limiters are shared by name across the process, so each run names its own
	o := &rlopts.Options{Name: t.Name() + "-" + strconv.FormatInt(time.Now().UnixNano(), 36), Limit: limit, Keys: keys}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	return o
}

func trackStatus(t *testing.T, s *session.Session) message.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	req, err := s.Open(ctx, &message.TrackStatus{Track: poolTrack("video")}, nopHandler{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := req.Response(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func limited(m message.Message) (uint64, bool) {
	re, ok := m.(*message.RequestError)
	if !ok || re.Code != message.RequestExcessiveLoad {
		return 0, false
	}
	return re.RetryInterval, true
}

func TestRequestRateLimit(t *testing.T) {
	org := startOrigin(t)
	lim := rateLimiter(t, 2, "client_ip")
	c := newBackendWith(t, "limited", "moqt://"+org.addr+"/live", func(o *bo.Options) {
		o.RateLimiterName, o.RateLimiter = lim.Name, lim
	})
	st := startListener(t, mo.NewListener(), c)
	s, err := dial(t, st.addr, "x")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, over := limited(trackStatus(t, s)); over {
			t.Fatal("a request under the limit is refused")
		}
	}
	retry, over := limited(trackStatus(t, s))
	if !over || retry < 2 {
		t.Errorf("a request over the limit is refused with the wait until it could pass: %v, %d", over, retry)
	}
	// a WebTransport session from the same address spends the same bucket
	addr := startWTEndpoint(t, http.HandlerFunc(c.serveWebTransport))
	_, ws, err := dialWT(t, addr, nil, alpn21)
	if err != nil {
		t.Fatal(err)
	}
	if _, over = limited(trackStatus(t, ws)); !over {
		t.Error("a WebTransport session's request is counted by the backend's limiter")
	}
}

func TestSessionRateLimit(t *testing.T) {
	org := startOrigin(t)
	c := newBackend(t, "sessions", "moqt://"+org.addr+"/live")
	lim := rateLimiter(t, 1, "client_ip")
	st := startListenerWith(t, mo.NewListener(), func(o *listener.Options) {
		o.RateLimiterName, o.RateLimiter = lim.Name, lim
	}, c)
	if _, err := dial(t, st.addr, "x"); err != nil {
		t.Fatalf("a session under the listener's limit is admitted: %v", err)
	}
	// the connection is closed before any session begins, so its reason never reaches the client
	refused := metrics.RateLimitDecisions.WithLabelValues(lim.Name, planeMoQ, ratelimit.ResultLimited.String())
	if _, err := dial(t, st.addr, "x"); err == nil {
		t.Error("a session over the limit is refused")
	}
	if n := testutil.ToFloat64(refused); n != 1 {
		t.Errorf("%v sessions counted as limited", n)
	}
}

func TestLimiterKeys(t *testing.T) {
	shared := newLimiter(rateLimiter(t, 1))
	if k := shared.keyFor(netip.Addr{}); !k.ok {
		t.Error("a limiter naming no keys counts every client in one bucket")
	}
	byIP := newLimiter(rateLimiter(t, 1, "client_ip"))
	if k := byIP.keyFor(netip.Addr{}); k.ok {
		t.Error("a session with no address lacks its key")
	}
	a, b := byIP.keyFor(netip.MustParseAddr("192.0.2.1")), byIP.keyFor(netip.MustParseAddr("192.0.2.2"))
	if !a.ok || a == b {
		t.Error("clients are counted apart")
	}
	counted := rateLimiter(t, 1)
	counted.Name += "-count"
	counted.Action = rlopts.ActionCount
	c := newLimiter(counted)
	for range 3 {
		if ok, _ := c.admit(c.keyFor(netip.Addr{})); !ok {
			t.Error("a count action records events without refusing them")
		}
	}
	if newLimiter(nil) != nil || limitRequests(nil, nil, netip.Addr{}) != nil {
		t.Error("no limiter wraps nothing")
	}
	if retryInterval(0) != 1 || retryInterval(-time.Second) != 1 || retryInterval(1500*time.Millisecond) != 1501 {
		t.Error("the retry interval is the wait in milliseconds plus one")
	}
}
