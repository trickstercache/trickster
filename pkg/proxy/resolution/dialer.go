/*
 * Copyright 2018 The Trickster Authors
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

// Package resolution dials a backend's origin by resolving its host as a DNS
// SRV owner name and connecting to a target from the answer (RFC 2782).
package resolution

import (
	"cmp"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	dnsclient "github.com/trickstercache/trickster/v2/pkg/dns/client"
	"github.com/trickstercache/trickster/v2/pkg/dns/resolver"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/resolution/options"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/compat"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	opDial        = "dial"
	attemptOK     = "success"
	attemptFailed = "failure"
	// minAttemptTimeout keeps one target's share of the connect timeout from shrinking to nothing
	minAttemptTimeout = 2 * time.Second
)

// Dialer resolves the dial host as an SRV owner name and connects to a target from its
// answer, failing over through the preferred tier and then the lower tiers
type Dialer struct {
	base         net.Dialer
	timeout      time.Duration
	res          resolver.Resolver
	srv          *ttlCache[*srvAnswer]
	addrs        *ttlCache[[]string]
	backend      string
	verifyTarget bool
	lookups      [resultCount]prometheus.Counter
	intn         func(int) int
}

type target struct {
	// host is the target name without its trailing dot, used as a TLS server name
	host string
	// fqdn is the lowercase target name with its trailing dot, keying the address cache
	fqdn string
	// ip is set when the target name is an IP literal, which is dialed without a lookup
	ip     []string
	port   string
	weight int
	tier   int
}

// srvAnswer is an SRV answer's usable targets, grouped into priority tiers, preferred first
type srvAnswer struct {
	tiers [][]target
	n     int
}

// New returns a Dialer for the backend's origin_resolution, or nil when it does not select srv.
// timeout bounds a whole dial, including the lookup and every failover attempt.
func New(backendName string, o *options.Options, keepAlive, timeout time.Duration) *Dialer {
	if !o.IsSRV() {
		return nil
	}
	var res resolver.Resolver
	if o.Resolver != "" {
		res = resolver.NewDirect(o.Resolver)
	} else {
		res = resolver.NewStd(nil)
	}
	return newDialer(backendName, o, res, keepAlive, timeout)
}

func newDialer(backendName string, o *options.Options, res resolver.Resolver,
	keepAlive, timeout time.Duration,
) *Dialer {
	minTTL, maxTTL, negativeTTL := o.TTLs()
	d := &Dialer{
		base:         net.Dialer{KeepAlive: keepAlive},
		timeout:      timeout,
		res:          res,
		backend:      backendName,
		verifyTarget: o.VerifiesTarget(),
		intn:         compat.IntN,
	}
	d.srv = newTTLCache(d.fetchSRV, minTTL, maxTTL, negativeTTL)
	d.addrs = newTTLCache(d.fetchAddrs, minTTL, maxTTL, negativeTTL)
	for i, name := range lookupResultNames {
		d.lookups[i] = metrics.OriginSRVLookups.WithLabelValues(backendName, name)
	}
	return d
}

// VerifiesTarget reports whether https dials must use DialTLSContext, which verifies the
// origin's certificate against the dialed SRV target rather than the URL host
func (d *Dialer) VerifiesTarget() bool {
	return d != nil && d.verifyTarget
}

// DialContext satisfies http.Transport.DialContext. The port in addr is ignored; an IP literal
// host has no SRV records and is dialed as given.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, _, err := d.dial(ctx, network, addr)
	return conn, err
}

// DialTLSContext dials like DialContext, then completes a TLS handshake that verifies the
// dialed SRV target; cfg is the transport's client TLS config and may be nil
func (d *Dialer) DialTLSContext(ctx context.Context, network, addr string,
	cfg *tls.Config,
) (net.Conn, error) {
	conn, serverName, err := d.dial(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	var c *tls.Config
	if cfg != nil {
		c = cfg.Clone()
	} else {
		c = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	c.ServerName = serverName
	tc := tls.Client(conn, c)
	hctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tc, nil
}

// dial returns the connection and the name of the host it reached
func (d *Dialer) dial(ctx context.Context, network, addr string) (net.Conn, string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	if _, err := netip.ParseAddr(host); err == nil {
		conn, err := d.base.DialContext(ctx, network, addr)
		return conn, host, err
	}
	answer, res, err := d.srv.get(ctx, strings.ToLower(strings.TrimSuffix(host, ".")))
	d.lookups[res].Inc()
	if err != nil {
		return nil, "", &net.OpError{
			Op: opDial, Net: network,
			Err: fmt.Errorf("srv lookup for %s: %w", host, err),
		}
	}
	targets := answer.order(d.intn)
	var lastErr error
	for i := range targets {
		t := &targets[i]
		conn, err := d.dialTarget(ctx, network, t, len(targets)-i)
		if err == nil {
			return conn, t.host, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, "", lastErr
}

// dialTarget tries each of the target's addresses, within its share of the time left
func (d *Dialer) dialTarget(ctx context.Context, network string, t *target,
	remaining int,
) (net.Conn, error) {
	addrs := t.ip
	var err error
	if addrs == nil {
		addrs, _, err = d.addrs.get(ctx, t.fqdn)
	}
	if err != nil {
		d.attempt(t.tier, false)
		return nil, &net.OpError{
			Op: opDial, Net: network,
			Err: fmt.Errorf("address lookup for srv target %s: %w", t.host, err),
		}
	}
	var lastErr error
	for _, a := range addrs {
		actx, cancel := context.WithTimeout(ctx, attemptTimeout(ctx, remaining))
		conn, err := d.base.DialContext(actx, network, net.JoinHostPort(a, t.port))
		cancel()
		d.attempt(t.tier, err == nil)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

func (d *Dialer) attempt(tier int, ok bool) {
	result := attemptFailed
	if ok {
		result = attemptOK
	}
	metrics.OriginSRVDialAttempts.WithLabelValues(d.backend, strconv.Itoa(tier), result).Inc()
}

// attemptTimeout splits the time left in ctx across the targets still to try
func attemptTimeout(ctx context.Context, remaining int) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		return minAttemptTimeout
	}
	left := time.Until(dl)
	if remaining <= 1 {
		return left
	}
	share := left / time.Duration(remaining)
	if share < minAttemptTimeout {
		share = min(minAttemptTimeout, left)
	}
	return share
}

// fetchSRV looks up name's SRV records, groups the usable targets into priority tiers, and
// primes the address cache with any addresses the server sent in the additional section
func (d *Dialer) fetchSRV(ctx context.Context, name string) (*srvAnswer, time.Duration, error) {
	answer, err := d.res.LookupSRV(ctx, dnsclient.Fqdn(name))
	if err != nil {
		return nil, 0, err
	}
	recs := make([]*dnsclient.SRV, 0, len(answer.Records))
	for _, r := range answer.Records {
		// a target of "." means the service is decidedly not available at that record
		if r.Target != "." && r.Target != "" {
			recs = append(recs, r)
		}
	}
	if len(recs) == 0 {
		return nil, 0, fmt.Errorf("%s: %w", name, errNoTargets)
	}
	slices.SortStableFunc(recs, func(a, b *dnsclient.SRV) int {
		return cmp.Compare(a.Priority, b.Priority)
	})
	out := &srvAnswer{n: len(recs)}
	for i, r := range recs {
		if i == 0 || r.Priority != recs[i-1].Priority {
			out.tiers = append(out.tiers, nil)
		}
		fqdn := strings.ToLower(dnsclient.Fqdn(r.Target))
		tier := len(out.tiers) - 1
		t := target{
			host:   strings.TrimSuffix(r.Target, "."),
			fqdn:   fqdn,
			port:   strconv.Itoa(int(r.Port)),
			weight: int(r.Weight),
			tier:   tier,
		}
		if _, err := netip.ParseAddr(t.host); err == nil {
			t.ip = []string{t.host}
		}
		out.tiers[tier] = append(out.tiers[tier], t)
		if ia, ok := answer.Additional[fqdn]; ok && len(ia.Addrs) > 0 {
			d.addrs.prime(fqdn, ia.Addrs, min(ia.TTL, answer.TTL))
		}
	}
	return out, answer.TTL, nil
}

func (d *Dialer) fetchAddrs(ctx context.Context, fqdn string) ([]string, time.Duration, error) {
	ia, err := d.res.LookupIP(ctx, fqdn)
	if err != nil {
		return nil, 0, err
	}
	if len(ia.Addrs) == 0 {
		return nil, 0, fmt.Errorf("%s: %w", fqdn, errNoTargets)
	}
	return ia.Addrs, ia.TTL, nil
}

// order returns the targets in dial order: the preferred tier first, each tier ordered by
// weighted random selection (RFC 2782). A lone target is returned without copying.
func (a *srvAnswer) order(intn func(int) int) []target {
	if a.n == 1 {
		return a.tiers[0]
	}
	out := make([]target, 0, a.n)
	for _, tier := range a.tiers {
		start := len(out)
		out = append(out, tier...)
		shuffleByWeight(out[start:], intn)
	}
	return out
}

// shuffleByWeight orders ts by repeated weighted random selection; zero-weight targets keep
// their places after every weighted one
func shuffleByWeight(ts []target, intn func(int) int) {
	sum := 0
	for i := range ts {
		sum += ts[i].weight
	}
	for sum > 0 && len(ts) > 1 {
		n := intn(sum)
		s := 0
		for i := range ts {
			s += ts[i].weight
			if s > n {
				ts[0], ts[i] = ts[i], ts[0]
				break
			}
		}
		sum -= ts[0].weight
		ts = ts[1:]
	}
}
