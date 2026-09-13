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

// Package docker implements the docker autodiscovery provider, reading
// containers from the Docker Engine API's GET /containers/json.
//
// # No SDK
//
// The one API call is hand-written. github.com/docker/docker is the
// Engine's own source tree rather than a client library: importing it for
// one list call pulls in a very large dependency surface. What this
// provider needs instead is a dialer for a unix socket, which is a dozen
// lines of net/http.
//
// # Endpoints, not hosts
//
// Unlike the cloud providers, the Engine API reports ports. A container
// exposing or publishing exactly one TCP port needs no 'port' in config;
// one exposing several must be told which, rather than having a guess
// made for it. UDP ports are never candidates, which is what makes the
// single-port case common enough to be worth resolving automatically.
//
// # Health
//
// GET /containers/json carries no Health object -- that lives only on the
// per-container inspect, which would cost one request per container per
// poll. Health appears in the list only inside the human-readable Status
// string, which is where this provider reads it from; see readiness in
// containers.go.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	dockeropts "github.com/trickstercache/trickster/v2/pkg/discovery/docker/options"
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

// maxResponseBytes bounds a container-list response. Container lists are
// small; this is high enough never to bind in practice and low enough
// that a misconfigured endpoint pointed at something enormous fails the
// poll instead of exhausting memory.
const maxResponseBytes = 32 << 20 // 32 MiB

// socketHost is the Host header used for unix-socket requests. The socket
// path is carried by the dialer, so the URL needs a syntactically valid
// but otherwise meaningless authority.
const socketHost = "docker"

// containersPath is the Engine API's container list operation
const containersPath = "/containers/json"

// maxErrorBytes bounds an error document read
const maxErrorBytes = 64 << 10

// URL schemes: the wire schemes a docker endpoint resolves to, and the
// default member scheme
const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// provider carries the discoverer's connection state; the shared
// discovery.Lifecycle owns Start/Stop/Subscribe
type provider struct {
	name     string
	endpoint string
	client   *http.Client
	http     *do.HTTPOptions
	docker   *dockeropts.Options
}

// New constructs the docker Discoverer; it satisfies
// discovery.NewDiscovererFunc.
func New(name string, o *do.Options) (discovery.Discoverer, error) {
	p, err := newProvider(name, o)
	if err != nil {
		return nil, err
	}
	return discovery.NewLifecycle(name, p.newSubscription), nil
}

// newProvider builds the provider's connection state. It performs no
// network I/O; a unix socket that does not exist yet is a dial failure on
// the first poll, not a startup failure.
func newProvider(name string, o *do.Options) (*provider, error) {
	if o == nil || o.Docker == nil {
		return nil, errors.New("docker discovery requires a 'docker' options block")
	}
	httpOpts := o.HTTP
	if httpOpts == nil {
		httpOpts = &do.HTTPOptions{}
	}
	host := httpOpts.Endpoint
	if host == "" {
		host = dockeropts.DefaultHost
	}
	endpoint, client, err := clientFor(host, httpOpts)
	if err != nil {
		return nil, err
	}
	return &provider{
		name: name, endpoint: endpoint, client: client,
		http: httpOpts, docker: o.Docker,
	}, nil
}

// clientFor builds the request base URL and the HTTP client for a Docker
// host, which may be a unix socket or a TCP endpoint.
//
// A socket needs a client whose dialer ignores the URL's authority, so it
// is built here rather than by the shared poller/http builder; a TCP
// endpoint goes through that builder so it inherits the shared TLS
// configuration, which is how a remote daemon's mutual TLS is set up.
func clientFor(host string, o *do.HTTPOptions) (string, *http.Client, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", nil, fmt.Errorf("docker endpoint %q is not a valid url: %w",
			host, err)
	}
	switch u.Scheme {
	case "unix":
		if u.Path == "" {
			return "", nil, fmt.Errorf(
				"docker endpoint %q names no socket path", host)
		}
		if o.TLS != nil {
			return "", nil, errors.New(
				"docker discovery cannot apply tls to a unix socket endpoint")
		}
		return schemeHTTP + "://" + socketHost, socketClient(u.Path, timeoutOf(o)), nil
	case "tcp":
		// tcp:// is the form DOCKER_HOST uses; it is http(s) on the wire,
		// and which one depends on whether TLS was configured
		scheme := schemeHTTP
		if o.TLS != nil {
			scheme = schemeHTTPS
		}
		u.Scheme = scheme
	case schemeHTTP, schemeHTTPS:
	default:
		return "", nil, fmt.Errorf(
			"docker endpoint %q must use the unix, tcp, http or https scheme",
			host)
	}
	if u.Host == "" {
		return "", nil, fmt.Errorf("docker endpoint %q names no host", host)
	}
	client, err := pollerhttp.NewClient(&pollerhttp.Options{
		URL:             u.String(),
		Timeout:         timeoutOf(o),
		TLS:             o.TLS,
		FollowRedirects: o.FollowRedirects,
		Transport: pollerhttp.TransportOptions{
			MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2,
		},
	})
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSuffix(u.String(), "/"), client, nil
}

// socketClient returns a client that dials a unix socket, ignoring the
// authority in the request URL.
func socketClient(path string, timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: timeout}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return d.DialContext(ctx, "unix", path)
			},
			// one daemon, one socket: a larger pool buys nothing
			MaxIdleConns:          1,
			MaxIdleConnsPerHost:   1,
			MaxConnsPerHost:       1,
			IdleConnTimeout:       pollerhttp.DefaultKeepAlive,
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func intervalOf(o *do.HTTPOptions) time.Duration {
	if o.Interval > 0 {
		return time.Duration(o.Interval)
	}
	return do.DefaultHTTPInterval
}

func timeoutOf(o *do.HTTPOptions) time.Duration {
	if o.Timeout > 0 {
		return time.Duration(o.Timeout)
	}
	return do.DefaultHTTPTimeout
}

// newSubscription builds a query's poll loop; it satisfies
// discovery.NewSubscriptionFunc
func (p *provider) newSubscription(q *do.Query,
	handler discovery.SnapshotHandler,
) (discovery.SubscriptionRunner, error) {
	scheme := q.Scheme
	if scheme == "" {
		scheme = schemeHTTP
	}
	target, err := p.listURL(q)
	if err != nil {
		return nil, err
	}
	s := &subscription{
		p:   p,
		url: target,
		mapping: mapping{
			scheme:            scheme,
			network:           q.Network,
			addressType:       q.AddressType,
			port:              q.Port,
			portLabel:         q.PortLabel,
			replicaGroupLabel: q.ReplicaGroupLabel,
		},
		emitter: discovery.NewEmitter(handler),
	}
	pl, err := poller.New(poller.Options{
		Name:     p.name,
		Interval: intervalOf(p.http),
		Timeout:  timeoutOf(p.http),
		OnPanic:  s.onPanic,
	}, s)
	if err != nil {
		return nil, err
	}
	s.poller = pl
	return s, nil
}

// listURL builds the container-list URL for a query, including the
// server-side filters.
func (p *provider) listURL(q *do.Query) (string, error) {
	filters, err := buildFilters(q.Filters)
	if err != nil {
		return "", err
	}
	v := url.Values{}
	v.Set("filters", filters)
	return p.endpoint + "/" + p.docker.GetAPIVersion() + containersPath +
		"?" + v.Encode(), nil
}

// buildFilters renders the query's filters as the Engine API's
// JSON-encoded filter document.
//
// A 'status' filter is added when the query names none, so that the
// daemon returns only running containers rather than every container that
// ever ran on the host. An operator who sets 'status' explicitly gets
// exactly what they asked for.
func buildFilters(qf map[string][]string) (string, error) {
	f := make(map[string][]string, len(qf)+1)
	maps.Copy(f, qf)
	if _, ok := f["status"]; !ok {
		f["status"] = []string{stateRunning}
	}
	b, err := json.Marshal(f)
	if err != nil {
		return "", fmt.Errorf("encoding docker filters: %w", err)
	}
	return string(b), nil
}

// subscription is one query's poll loop; it implements
// discovery.SubscriptionRunner
type subscription struct {
	p       *provider
	url     string
	mapping mapping
	emitter *discovery.Emitter
	poller  *poller.Poller

	mtx     sync.Mutex
	stopped bool
	failing bool
	// skippedLogged suppresses repeated identical exclusion warnings, so a
	// permanently unmappable container is reported once rather than every
	// poll forever
	skippedLogged string
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

// Poll performs one container list and emits the resulting membership; it
// implements poller.Source. Every failure keeps the last-good membership.
func (s *subscription) Poll(ctx context.Context) (time.Duration, error) {
	cs, err := s.list(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// stopped mid-poll; not a refresh failure
			return 0, err
		}
		s.warn(err)
		return 0, err
	}
	s.clearWarn()
	snap, skipped := toMembers(cs, s.mapping)
	s.reportSkipped(skipped)
	s.emitter.Emit(snap)
	return 0, nil // defer to the configured interval
}

// list performs the container-list request and decodes it
func (s *subscription) list(ctx context.Context) ([]container, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", "application/json")
	resp, err := s.p.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	body, err := tbytes.ReadBoundedBody(resp.Body, maxResponseBytes, false)
	if err != nil {
		return nil, err
	}
	return parseContainers(body)
}

// dockerError is the Engine API's error document
type dockerError struct {
	Message string `json:"message"`
}

// checkStatus converts a non-2xx response into an error carrying whatever
// the daemon said went wrong, which is how a bad filter name or an
// unsupported api_version surfaces legibly.
func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// truncate is deliberately false: the truncating mode reads a
	// fixed-length prefix and fails a short read, which for an error
	// document -- almost always well under a kilobyte -- discards the
	// very message this function exists to surface. Oversized bodies
	// return an error here and fall through to the status-only message.
	body, _ := tbytes.ReadBoundedBody(resp.Body, maxErrorBytes, false)
	var e dockerError
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return fmt.Errorf("docker api error (http %d): %s",
			resp.StatusCode, e.Message)
	}
	return fmt.Errorf("docker api returned http %d", resp.StatusCode)
}

// reportSkipped logs containers that could not become members, once per
// distinct set rather than every poll
func (s *subscription) reportSkipped(skipped []excluded) {
	if len(skipped) == 0 {
		s.mtx.Lock()
		s.skippedLogged = ""
		s.mtx.Unlock()
		return
	}
	var sb strings.Builder
	for i, e := range skipped {
		if i > 0 {
			sb.WriteString("; ")
		}
		sb.WriteString(e.name)
		sb.WriteString(": ")
		sb.WriteString(e.reason)
	}
	detail := sb.String()
	s.mtx.Lock()
	same := s.skippedLogged == detail
	s.skippedLogged = detail
	s.mtx.Unlock()
	if same {
		return
	}
	discovery.LogWarn("docker discovery excluded containers that could not become members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Detail:     detail,
		})
}

// warn counts a refresh failure and logs it once per failure streak
func (s *subscription) warn(err error) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.Docker).Inc()
	s.mtx.Lock()
	failing := s.failing
	s.failing = true
	s.mtx.Unlock()
	if failing {
		return
	}
	discovery.LogWarn("docker discovery refresh failed; keeping last-good members",
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
	discovery.LogInfo("docker discovery refresh recovered",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.URL:        discovery.SanitizeURL(s.url),
		})
}

// onPanic reports a panicking poll as a refresh error, so a provider bug
// surfaces on the same metric and log stream as an upstream failure
// rather than silently freezing the membership.
func (s *subscription) onPanic(r any, stack []byte) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.Docker).Inc()
	discovery.LogError("panic during docker discovery refresh; keeping last-good members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.URL:        discovery.SanitizeURL(s.url),
			keys.Panic:      fmt.Sprintf("%v", r),
			keys.Stack:      string(stack),
		})
}
