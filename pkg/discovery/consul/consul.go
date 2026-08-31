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

// Package consul implements the consul autodiscovery provider, reading
// service instances from Consul's health endpoint.
//
// It is event-driven rather than polled. Each request is a Consul blocking
// query: the server parks it until the service's index changes or the
// configured wait elapses, so a membership change is observed within a
// round trip instead of within a poll interval, and a stable service costs
// one parked connection rather than a request per interval. This is why the
// provider is a thin client rather than a wrapper around Consul's Go
// library -- the blocking-query loop is twenty lines and maps directly onto
// the shared poller, and the library would add a dependency to reach the
// same place.
//
// Because Consul reports per-instance check status, this is the first
// provider outside kubernetes that can honestly answer 'is this member
// ready', making health_mode: provider meaningful for VM and container
// fleets registered in Consul.
package consul

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/discovery/blockingquery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/poller"
	pollerhttp "github.com/trickstercache/trickster/v2/pkg/discovery/poller/http"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

// ErrStopped aliases discovery.ErrStopped for callers of this package
var ErrStopped = discovery.ErrStopped

// ErrNoHTTPOptions is returned when a discoverer is constructed without the
// shared http options block, which carries the Consul address
var ErrNoHTTPOptions = errors.New("consul discovery requires an 'http' options block")

const (
	// healthServicePath is the catalog endpoint, to which the service name
	// is appended
	healthServicePath = "/v1/health/service/"
	// indexHeader carries the blocking-query cursor
	indexHeader = "X-Consul-Index"
	// minPollGap is the shortest time between successive blocking queries;
	// see blockingquery.Cursor for why a floor is required
	minPollGap = 100 * time.Millisecond
	// maxResponseBytes bounds a catalog response. Large services are in the
	// low megabytes; this is high enough never to bind in practice and low
	// enough that an endpoint pointed at something else fails the poll
	// rather than exhausting memory.
	maxResponseBytes = 32 << 20
)

// provider carries the discoverer's connection-level settings; the shared
// discovery.Lifecycle owns Start/Stop/Subscribe
type provider struct {
	name     string
	endpoint *url.URL
	http     *do.HTTPOptions
	consul   *do.ConsulOptions
}

// New constructs the consul Discoverer; it satisfies
// discovery.NewDiscovererFunc.
func New(name string, o *do.Options) (discovery.Discoverer, error) {
	if o == nil || o.HTTP == nil {
		return nil, ErrNoHTTPOptions
	}
	u, err := url.Parse(o.HTTP.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("consul endpoint %q is not a valid url: %w",
			o.HTTP.Endpoint, err)
	}
	c := o.Consul
	if c == nil {
		c = &do.ConsulOptions{}
	}
	p := &provider{name: name, endpoint: u, http: o.HTTP, consul: c}
	return discovery.NewLifecycle(name, p.newSubscription), nil
}

// interval returns the cadence used when a blocking query is not in effect:
// the retry delay after a failure, and the pause after a non-blocking
// first read that did not yield a usable index.
func (p *provider) interval() time.Duration {
	if p.http.Interval > 0 {
		return time.Duration(p.http.Interval)
	}
	return do.DefaultHTTPInterval
}

// timeout returns the bound on one poll, which must outlast the blocking
// wait plus Consul's own jitter.
func (p *provider) timeout() time.Duration {
	if p.http.Timeout > 0 {
		return time.Duration(p.http.Timeout)
	}
	return do.ConsulPollTimeout(p.consul.GetWait())
}

// newSubscription builds a query's blocking-query loop; it satisfies
// discovery.NewSubscriptionFunc
func (p *provider) newSubscription(q *do.Query,
	handler discovery.SnapshotHandler,
) (discovery.SubscriptionRunner, error) {
	if q.Service == "" {
		return nil, errors.New("consul discovery query requires a service")
	}
	scheme := q.Scheme
	if scheme == "" {
		scheme = "http"
	}
	s := &subscription{
		p:       p,
		service: q.Service,
		url:     p.endpoint.JoinPath(healthServicePath, q.Service).String(),
		base:    p.baseParams(q),
		mapping: mapping{
			scheme:            scheme,
			replicaGroupLabel: q.ReplicaGroupLabel,
			warningIsReady:    p.consul.GetWarningIsReady(),
		},
		cursor:  blockingquery.NewCursor(minPollGap),
		emitter: discovery.NewEmitter(handler),
	}
	src, err := pollerhttp.NewSource(&pollerhttp.Options{
		URL:             s.url,
		Method:          http.MethodGet,
		Headers:         p.staticHeaders(),
		Timeout:         p.timeout(),
		TLS:             p.http.TLS,
		FollowRedirects: p.http.FollowRedirects,
		Decorate:        s.decorate,
	}, s.handle)
	if err != nil {
		return nil, err
	}
	s.src = src
	pl, err := poller.New(poller.Options{
		Name:     p.name,
		Interval: p.interval(),
		// the iteration context must outlast the blocking wait; poller/http
		// deliberately holds no second, shorter deadline underneath it
		Timeout: p.timeout(),
		OnPanic: s.onPanic,
	}, s)
	if err != nil {
		return nil, err
	}
	s.poller = pl
	return s, nil
}

// baseParams builds the query parameters that never change between polls.
func (p *provider) baseParams(q *do.Query) url.Values {
	v := url.Values{}
	if p.consul.Datacenter != "" {
		v.Set("dc", p.consul.Datacenter)
	}
	if p.consul.Namespace != "" {
		v.Set("ns", p.consul.Namespace)
	}
	if p.consul.Partition != "" {
		v.Set("partition", p.consul.Partition)
	}
	if p.consul.AllowStale {
		// a valueless parameter, per Consul's convention
		v.Set("stale", "")
	}
	if p.consul.OnlyPassing {
		v.Set("passing", "true")
	}
	if q.Filter != "" {
		v.Set("filter", q.Filter)
	}
	for _, tag := range q.Tags {
		v.Add("tag", tag)
	}
	return v
}

// staticHeaders returns the headers sent on every poll. Consul accepts the
// Authorization Bearer scheme as an equivalent to X-Consul-Token, so a
// token configured either way reaches it.
func (p *provider) staticHeaders() map[string]string {
	out := make(map[string]string, len(p.http.Headers)+1)
	maps.Copy(out, p.http.Headers)
	if p.http.BearerToken != "" {
		out["Authorization"] = "Bearer " + p.http.BearerToken
	}
	return out
}

// subscription is one service's blocking-query loop; it implements
// discovery.SubscriptionRunner and poller.Source
type subscription struct {
	p       *provider
	service string
	url     string
	base    url.Values
	mapping mapping
	emitter *discovery.Emitter
	poller  *poller.Poller
	src     poller.Source

	// cursor owns the blocking-query index and the floor between requests
	cursor *blockingquery.Cursor

	mtx     sync.Mutex
	stopped bool
	failing bool
}

// Launch starts the query's blocking-query loop
func (s *subscription) Launch(ctx context.Context) {
	s.mtx.Lock()
	stopped := s.stopped
	s.mtx.Unlock()
	if stopped {
		return
	}
	s.poller.Start(ctx)
}

// Stop terminates the loop and suppresses further emissions
func (s *subscription) Stop() {
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	s.stopped = true
	s.mtx.Unlock()
	s.emitter.Stop()
	s.poller.Stop()
}

// Poll implements poller.Source, wrapping the HTTP source so that one place
// owns failure accounting. Transport and credential errors return before the
// response handler runs, so accounting done there would miss exactly the
// failures an operator most needs to see.
func (s *subscription) Poll(ctx context.Context) (time.Duration, error) {
	s.cursor.Begin()
	next, err := s.src.Poll(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return 0, err
		}
		// a failed blocking query may have failed because the cursor is no
		// longer meaningful (the server was replaced, the service was
		// recreated); dropping it makes the retry a plain read that always
		// yields a usable answer, rather than a blocking query that may
		// park forever against a stale index
		s.cursor.Reset()
		s.warn(err)
		// fall back to the configured interval as the retry cadence
		return 0, err
	}
	s.clearWarn()
	return next, nil
}

// decorate applies the per-poll request state: the blocking-query cursor,
// and a token re-read from disk so a rotated credential is picked up
// without a restart.
func (s *subscription) decorate(_ context.Context, r *http.Request) error {
	if f := s.p.http.BearerTokenFile; f != "" {
		token, err := readTokenFile(f)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if s.p.http.Username != "" {
		r.SetBasicAuth(s.p.http.Username, s.p.http.Password)
	}
	v := make(url.Values, len(s.base)+2)
	maps.Copy(v, s.base)
	if index := s.cursor.Index(); index > 0 {
		// only a request carrying a cursor blocks; the first read of a
		// subscription must return immediately so the pool fills
		v.Set("index", strconv.FormatUint(index, 10))
		v.Set("wait", blockingquery.Duration(s.p.consul.GetWait()))
	}
	r.URL.RawQuery = v.Encode()
	return nil
}

// handle interprets one response, emitting on change and returning an error
// otherwise; failure accounting belongs to Poll.
func (s *subscription) handle(_ context.Context, resp *http.Response) (time.Duration, error) {
	if err := pollerhttp.CheckStatus(resp); err != nil {
		return 0, err
	}
	had, changed := s.cursor.Advance(resp.Header.Get(indexHeader))
	if had && !changed {
		// the blocking query timed out with the service unchanged: the body
		// is identical to the one already applied, so skip decoding it
		// entirely. This is the saving that makes a long wait cheap.
		return s.cursor.NextWait(), nil
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return 0, err
	}
	snap, err := parseCatalog(body, s.mapping)
	if err != nil {
		return 0, fmt.Errorf("catalog did not parse: %w", err)
	}
	s.emitter.Emit(snap)
	return s.cursor.NextWait(), nil
}

// warn counts a refresh failure and logs it once per failure streak
func (s *subscription) warn(err error) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.Consul).Inc()
	s.mtx.Lock()
	failing := s.failing
	s.failing = true
	s.mtx.Unlock()
	if failing {
		return
	}
	discovery.LogWarn("consul discovery refresh failed; keeping last-good members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Service:    s.service,
			keys.URL:        discovery.SanitizeURL(s.url),
			keys.Error:      err.Error(),
		})
}

// clearWarn ends a failure streak, logging the recovery once
func (s *subscription) clearWarn() {
	s.mtx.Lock()
	if !s.failing {
		s.mtx.Unlock()
		return
	}
	s.failing = false
	s.mtx.Unlock()
	discovery.LogInfo("consul discovery refresh recovered",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Service:    s.service,
		})
}

// onPanic reports a panicking poll as a refresh error, so a provider bug
// surfaces on the same metric and log stream as an upstream failure rather
// than silently freezing the membership.
func (s *subscription) onPanic(r any, stack []byte) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.Consul).Inc()
	discovery.LogError("panic during consul discovery refresh; keeping last-good members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Service:    s.service,
			keys.Panic:      fmt.Sprintf("%v", r),
			keys.Stack:      string(stack),
		})
}
