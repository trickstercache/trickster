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

// Package httpsd implements the http_sd autodiscovery provider: a member
// list fetched from an HTTP endpoint on a poll cadence. It is the universal
// adapter. Any service-discovery system Trickster has no in-tree provider
// for can be exposed through a few lines of glue that serves a member list
//
// Two document formats are accepted, named explicitly by
// discovery.<name>.http_sd.format: Trickster's native member list (the same
// document the file provider reads, carrying scheme, path prefix, weight
// and replica group), and Prometheus's file_sd/http_sd JSON, so an existing
// http_sd endpoint can be pointed at Trickster unchanged.
//
// The endpoint is polled on discovery.<name>.http.interval. Requests carry
// X-Prometheus-Refresh-Interval-Seconds for compatibility with servers that
// tune their own work to the client's cadence, and honor ETag: a 304
// response is a no-op that costs one conditional request. Every failure --
// a transport error, an unexpected status, an oversized body, a document
// that will not parse -- keeps the last-good membership and is logged once
// per failure streak.
package httpsd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/discovery/memberlist"
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
// shared http options block, which carries the endpoint
var ErrNoHTTPOptions = errors.New("http_sd discovery requires an 'http' options block")

// refreshIntervalHeader tells the server the client's poll cadence.
// Prometheus sends it, and servers that generate member lists on demand use
// it to pace their own work, so sending it makes an existing http_sd
// endpoint behave the same way for Trickster as it does for Prometheus.
const refreshIntervalHeader = "X-Prometheus-Refresh-Interval-Seconds"

// maxResponseBytes bounds a member-list response. Member lists are small;
// this is high enough to never bind in practice and low enough that a
// misconfigured endpoint pointed at something enormous fails the poll
// instead of exhausting memory.
const maxResponseBytes = 16 << 20 // 16 MiB

// provider carries the discoverer's connection-level settings; the shared
// discovery.Lifecycle owns Start/Stop/Subscribe
type provider struct {
	name     string
	endpoint *url.URL
	format   string
	http     *do.HTTPOptions
}

// New constructs the http_sd Discoverer; it satisfies
// discovery.NewDiscovererFunc.
func New(name string, o *do.Options) (discovery.Discoverer, error) {
	if o == nil || o.HTTP == nil {
		return nil, ErrNoHTTPOptions
	}
	u, err := url.Parse(o.HTTP.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("http_sd endpoint %q is not a valid url: %w",
			o.HTTP.Endpoint, err)
	}
	p := &provider{
		name:     name,
		endpoint: u,
		format:   o.HTTPSD.GetFormat(),
		http:     o.HTTP,
	}
	return discovery.NewLifecycle(name, p.newSubscription), nil
}

// interval returns the configured poll cadence, or the default.
func (p *provider) interval() time.Duration {
	if p.http.Interval > 0 {
		return time.Duration(p.http.Interval)
	}
	return do.DefaultHTTPInterval
}

// timeout returns the configured single-poll bound, or the default.
func (p *provider) timeout() time.Duration {
	if p.http.Timeout > 0 {
		return time.Duration(p.http.Timeout)
	}
	return do.DefaultHTTPTimeout
}

// newSubscription builds a query's poll-loop runner; it satisfies
// discovery.NewSubscriptionFunc
func (p *provider) newSubscription(q *do.Query,
	handler discovery.SnapshotHandler,
) (discovery.SubscriptionRunner, error) {
	target := *p.endpoint
	if q.Path != "" {
		// the query's path selects one member list from an endpoint that
		// can serve several, so that ALBs with different pools can share
		// one discoverer's connection settings
		target = *p.endpoint.JoinPath(q.Path)
	}
	s := &subscription{
		p:       p,
		url:     target.String(),
		scheme:  q.Scheme,
		emitter: discovery.NewEmitter(handler),
	}
	src, err := pollerhttp.NewSource(&pollerhttp.Options{
		URL:             s.url,
		Method:          http.MethodGet,
		Headers:         p.requestHeaders(),
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
		// the iteration context owns the deadline; poller/http deliberately
		// holds no second one
		Timeout: p.timeout(),
		OnPanic: s.onPanic,
	}, s)
	if err != nil {
		return nil, err
	}
	s.poller = pl
	return s, nil
}

// requestHeaders returns the static headers sent on every poll.
func (p *provider) requestHeaders() map[string]string {
	out := make(map[string]string, len(p.http.Headers)+2)
	maps.Copy(out, p.http.Headers)
	out[refreshIntervalHeader] = strconv.Itoa(int(p.interval().Seconds()))
	if p.http.BearerToken != "" {
		out["Authorization"] = "Bearer " + p.http.BearerToken
	}
	return out
}

// subscription is one query's poll loop; it implements
// discovery.SubscriptionRunner
type subscription struct {
	p       *provider
	url     string
	scheme  string
	emitter *discovery.Emitter
	poller  *poller.Poller
	src     poller.Source

	mtx     sync.Mutex
	stopped bool
	failing bool
	// etag is the validator from the last successfully parsed response. It
	// is only stored after a parse succeeds: storing it earlier would let a
	// subsequent 304 confirm a document we rejected.
	etag string
}

// Poll implements poller.Source, wrapping the HTTP source so that one place
// owns failure accounting.
//
// This wrapper is not incidental. A transport error (endpoint down, DNS
// failure, TLS rejection) or a decorator error (an unreadable
// bearer_token_file) returns before the response handler ever runs, so
// accounting done inside the handler would miss precisely the failures an
// operator most needs to see -- and the provider would keep serving its
// last-good membership with no metric and no log to say why.
func (s *subscription) Poll(ctx context.Context) (time.Duration, error) {
	next, err := s.src.Poll(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// stopped mid-poll; not a refresh failure
			return 0, err
		}
		s.warn(err)
		return 0, err
	}
	s.clearWarn()
	return next, nil
}

// Launch starts the query's poll loop
func (s *subscription) Launch(ctx context.Context) {
	s.mtx.Lock()
	stopped := s.stopped
	s.mtx.Unlock()
	if stopped {
		return
	}
	s.poller.Start(ctx)
}

// Stop terminates the poll loop and suppresses further emissions
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

// decorate applies the per-poll request state: the conditional-request
// validator, and a bearer token re-read from disk so that a rotated
// credential is picked up without a restart.
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
	s.mtx.Lock()
	etag := s.etag
	s.mtx.Unlock()
	if etag != "" {
		r.Header.Set("If-None-Match", etag)
	}
	return nil
}

// handle interprets one response, emitting on success and returning an
// error otherwise; failure accounting belongs to Poll. Every failure path
// keeps the last-good membership: this provider never emits an empty
// snapshot to report an error, only to report an endpoint that
// authoritatively returned none.
func (s *subscription) handle(_ context.Context, resp *http.Response) (time.Duration, error) {
	if resp.StatusCode == http.StatusNotModified {
		// the membership we already hold is still current; the whole point
		// of sending the validator
		return 0, nil
	}
	if err := pollerhttp.CheckStatus(resp); err != nil {
		return 0, err
	}
	body, err := readBody(resp.Body)
	if err != nil {
		return 0, err
	}
	snap, err := s.parse(body)
	if err != nil {
		return 0, fmt.Errorf("member list did not parse: %w", err)
	}
	s.mtx.Lock()
	s.etag = resp.Header.Get("ETag")
	s.mtx.Unlock()
	s.emitter.Emit(snap)
	return 0, nil // defer to the configured interval
}

// parse decodes the body in the discoverer's configured format.
func (s *subscription) parse(body []byte) (discovery.Snapshot, error) {
	if s.p.format == do.FormatPrometheus {
		return memberlist.ParsePrometheus(body, s.scheme)
	}
	return memberlist.Parse(body)
}

// readBody reads a bounded response body, failing rather than truncating so
// that an oversized document is never applied as a partial membership.
func readBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxResponseBytes {
		return nil, fmt.Errorf("member list exceeds the %d byte limit",
			maxResponseBytes)
	}
	return b, nil
}

// warn counts a refresh failure and logs it once per failure streak
func (s *subscription) warn(err error) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.HTTPSD).Inc()
	s.mtx.Lock()
	failing := s.failing
	s.failing = true
	s.mtx.Unlock()
	if failing {
		return
	}
	discovery.LogWarn("http_sd discovery refresh failed; keeping last-good members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
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
	discovery.LogInfo("http_sd discovery refresh recovered",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.URL:        discovery.SanitizeURL(s.url),
		})
}

// onPanic reports a panicking poll as a refresh error, so a provider bug
// surfaces on the same metric and log stream as an upstream failure rather
// than silently freezing the membership.
func (s *subscription) onPanic(r any, stack []byte) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.HTTPSD).Inc()
	discovery.LogError("panic during http_sd discovery refresh; keeping last-good members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.URL:        discovery.SanitizeURL(s.url),
			keys.Panic:      fmt.Sprintf("%v", r),
			keys.Stack:      string(stack),
		})
}
