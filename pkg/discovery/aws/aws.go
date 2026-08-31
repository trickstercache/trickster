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

// Package aws implements the aws autodiscovery provider, reading members
// from an AWS API selected by discovery.<name>.aws.service.
//
// # One provider, several services
//
// 'service' is required and selects which API to read -- ec2 for instances,
// ecs for tasks (including Fargate, which ec2 structurally cannot see) --
// and doubles as the SigV4 signing service name. Each is a serviceLister; new AWS sources
// arrive as new service values rather than new providers, inheriting this
// provider's credentials, signing, poll loop, options block and failure
// accounting rather than restating them.
//
// # No service clients
//
// The API calls here are hand-written against a signed HTTP client.
// aws-sdk-go-v2's generated service clients are deliberately not used:
// service/ec2 alone contributes roughly 12 MiB to a binary and is known to
// exhaust the compiler on small machines, all to make one list call. What
// this package does take from the SDK is the credential chain, via pkg/aws
// -- that is genuinely hard to get right, and getting it wrong means
// authenticating as the wrong principal. See
// trickster-data/decision-aws-dependencies.md.
//
// # Hosts, not endpoints
//
// An AWS inventory returns hosts with addresses but no port. The query's
// port or port_label supplies one, and for ec2 address_type chooses which
// address. A resource that cannot yield either is excluded and reported,
// rather than failing the whole refresh: unlike a service registry, where
// every entry is by definition an instance of the service, an inventory
// routinely contains resources that are simply not tagged yet.
package aws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	awsopts "github.com/trickstercache/trickster/v2/pkg/discovery/aws/options"
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

const (
	// maxResponseBytes bounds one API response page
	maxResponseBytes = 32 << 20
	// maxPages bounds pagination, so a pathological NextToken loop cannot
	// spin forever inside a single poll
	maxPages = 100
)

// provider carries the discoverer's connection-level settings; the shared
// discovery.Lifecycle owns Start/Stop/Subscribe
type provider struct {
	name     string
	service  string
	endpoint string
	client   *http.Client
	signer   *taws.Signer
	http     *do.HTTPOptions
	// pageSize caps instances per response page. Zero omits the parameter,
	// letting EC2 use its own default, which is what production wants: a
	// smaller page only adds round trips. It exists so tests can force
	// real pagination against a handful of instances rather than needing a
	// thousand.
	pageSize int
}

// New constructs the aws Discoverer; it satisfies
// discovery.NewDiscovererFunc.
func New(name string, o *do.Options) (discovery.Discoverer, error) {
	p, err := newProvider(name, o)
	if err != nil {
		return nil, err
	}
	return discovery.NewLifecycle(name, p.newSubscription), nil
}

// newProvider builds the provider's connection state.
func newProvider(name string, o *do.Options) (*provider, error) {
	if o == nil || o.AWS == nil {
		return nil, errors.New("aws discovery requires an 'aws' options block")
	}
	service := o.AWS.Service
	if service == "" {
		return nil, fmt.Errorf(
			"aws discovery requires 'aws.service'; supported services are %s",
			strings.Join(awsopts.SupportedServices(), ", "))
	}
	if !slices.Contains(awsopts.SupportedServices(), service) {
		return nil, fmt.Errorf(
			"aws discovery service %q is not supported; supported services are %s",
			service, strings.Join(awsopts.SupportedServices(), ", "))
	}
	signer, err := taws.NewSigner(o.AWS.SignerOptions())
	if err != nil {
		return nil, err
	}
	httpOpts := o.HTTP
	if httpOpts == nil {
		httpOpts = &do.HTTPOptions{}
	}
	endpoint, err := resolveEndpoint(httpOpts.Endpoint, service, o.AWS.Region)
	if err != nil {
		return nil, err
	}
	// a paginated call makes several requests per poll, so it drives the
	// client directly rather than through a one-request-per-iteration
	// Source; the client is still the shared one, so TLS and connection
	// pooling behave as they do everywhere else
	client, err := pollerhttp.NewClient(&pollerhttp.Options{
		URL:             endpoint,
		Timeout:         timeoutOf(httpOpts),
		TLS:             httpOpts.TLS,
		FollowRedirects: httpOpts.FollowRedirects,
		// several round trips per poll, so keep the connection warm
		Transport: pollerhttp.TransportOptions{
			MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2,
		},
	})
	if err != nil {
		return nil, err
	}
	return &provider{
		name: name, service: service, endpoint: endpoint,
		client: client, signer: signer, http: httpOpts,
	}, nil
}

// resolveEndpoint returns the configured override, or the regional endpoint
// for the service.
//
// The region matters here even though credentials resolve it later: an
// endpoint must be chosen before the first request, and there is no
// meaningful default region to fall back on.
func resolveEndpoint(override, service, region string) (string, error) {
	if override != "" {
		u, err := url.Parse(override)
		if err != nil {
			return "", fmt.Errorf("aws endpoint %q is not a valid url: %w", override, err)
		}
		if u.Scheme == "" || u.Host == "" {
			return "", fmt.Errorf("aws endpoint %q must be an absolute url", override)
		}
		return override, nil
	}
	if region == "" {
		return "", errors.New(
			"aws discovery requires 'aws.region' when no endpoint override is set, " +
				"because the API endpoint is derived from it")
	}
	return "https://" + service + "." + region + ".amazonaws.com", nil
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
		scheme = "http"
	}
	s := &subscription{p: p, emitter: discovery.NewEmitter(handler)}
	m := mapping{
		scheme:            scheme,
		addressType:       q.GetAddressType(),
		port:              q.Port,
		portLabel:         q.PortLabel,
		replicaGroupLabel: q.ReplicaGroupLabel,
		tags:              q.Tags,
	}
	switch p.service {
	case awsopts.ServiceECS:
		s.lister = &ecsLister{p: p, q: q, mapping: m}
	default:
		s.lister = &ec2Lister{p: p, filters: q.Filters, mapping: m}
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

// subscription is one query's poll loop; it implements
// discovery.SubscriptionRunner and poller.Source
type subscription struct {
	p       *provider
	lister  serviceLister
	emitter *discovery.Emitter
	poller  *poller.Poller

	mtx     sync.Mutex
	stopped bool
	failing bool
	// skippedLogged suppresses repeated identical exclusion warnings, so a
	// permanently mis-tagged instance is reported once rather than every
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

// Poll implements poller.Source: one poll is however many API calls the
// configured service needs, applied together.
func (s *subscription) Poll(ctx context.Context) (time.Duration, error) {
	snap, skipped, err := s.lister.Members(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return 0, err
		}
		s.warn(err)
		return 0, err
	}
	s.reportSkipped(skipped)
	s.clearWarn()
	s.emitter.Emit(snap)
	return 0, nil // defer to the configured interval
}

// serviceLister is one AWS API's discovery implementation. Adding a service
// means adding a lister, not a provider: credentials, signing, the poll
// loop, the options block and the failure accounting are all shared.
type serviceLister interface {
	// Members performs whatever calls the API needs and returns the
	// resulting membership, plus any resources that could not become
	// members and why.
	Members(ctx context.Context) (discovery.Snapshot, []excluded, error)
}

// post sends a signed request to the service endpoint and returns the
// response body, or an error carrying whatever the API said went wrong.
func (p *provider) post(ctx context.Context, body string,
	headers map[string]string,
) ([]byte, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint,
		strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	r.ContentLength = int64(len(body))
	if err := p.signer.SignRequest(ctx, r); err != nil {
		return nil, err
	}
	resp, err := p.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer func() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}()
	payload, err := tbytes.ReadBoundedBody(resp.Body, maxResponseBytes, false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, p.apiError(resp.StatusCode, payload)
	}
	return payload, nil
}

// apiError renders whichever error document the service uses, which carries
// a far more useful message than the status code alone.
func (p *provider) apiError(status int, payload []byte) error {
	if p.service == awsopts.ServiceECS {
		return jsonAPIError(status, payload)
	}
	return apiError(status, payload)
}

// apiError renders a Query-protocol error document, which carries a far
// more useful message than the status code alone.
func apiError(status int, payload []byte) error {
	var e ec2ErrorResponse
	if err := xmlUnmarshal(payload, &e); err == nil && len(e.Errors) > 0 {
		return fmt.Errorf("EC2 API error (http %d): %s", status, e.Error())
	}
	return fmt.Errorf("EC2 API returned http %d", status)
}

// reportSkipped logs instances that could not become members. They are not
// an error -- the pool is still correct for everything that could -- but
// they are a misconfiguration an operator needs to see.
func (s *subscription) reportSkipped(skipped []excluded) {
	if len(skipped) == 0 {
		s.mtx.Lock()
		s.skippedLogged = ""
		s.mtx.Unlock()
		return
	}
	summary := summarize(skipped)
	s.mtx.Lock()
	repeat := s.skippedLogged == summary
	s.skippedLogged = summary
	s.mtx.Unlock()
	if repeat {
		return
	}
	discovery.LogWarn("aws discovery excluded instances that could not become members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Detail:     summary,
		})
}

// warn counts a refresh failure and logs it once per failure streak
func (s *subscription) warn(err error) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.AWS).Inc()
	s.mtx.Lock()
	failing := s.failing
	s.failing = true
	s.mtx.Unlock()
	if failing {
		return
	}
	discovery.LogWarn("aws discovery refresh failed; keeping last-good members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Service:    s.p.service,
			keys.URL:        discovery.SanitizeURL(s.p.endpoint),
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
	discovery.LogInfo("aws discovery refresh recovered",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Service:    s.p.service,
		})
}

// onPanic reports a panicking poll as a refresh error, so a provider bug
// surfaces on the same metric and log stream as an upstream failure rather
// than silently freezing the membership.
func (s *subscription) onPanic(r any, stack []byte) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.AWS).Inc()
	discovery.LogError("panic during aws discovery refresh; keeping last-good members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Service:    s.p.service,
			keys.Panic:      fmt.Sprintf("%v", r),
			keys.Stack:      string(stack),
		})
}
