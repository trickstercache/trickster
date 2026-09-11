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

// Package gcp implements the gcp autodiscovery provider, reading members
// from a Google Cloud API selected by discovery.<name>.gcp.service.
//
// # One provider, several services
//
// 'service' is required and selects which Google Cloud product to read.
// Only 'gce' exists today -- Compute Engine instances via the Compute API's
// instances.aggregatedList -- and the field is required all the same, so
// that later services are chosen rather than opted out of. The provider is
// named for the cloud, not for Compute Engine, matching 'aws' and 'azure':
// Google Cloud products outside Compute Engine belong here too, and would
// sit oddly under a provider called 'gce'.
//
// # No generated client
//
// The one API call is hand-written against an OAuth2-authorized HTTP
// client. google.golang.org/api/compute/v1 is deliberately not used: it is
// a very large generated package, and this provider needs one list method
// from it. What is taken from Google's libraries is credential resolution
// -- golang.org/x/oauth2/google, which understands Application Default
// Credentials, Workload Identity on GKE, and the GCE metadata server --
// because that is the part genuinely worth not reimplementing.
//
// # Aggregated across zones
//
// instances.aggregatedList covers every zone in the project in one paged
// call, so no zone list has to be configured or kept current. Narrowing is
// done with the query's server-side filter expression and network tags.
//
// # Hosts, not endpoints
//
// A compute instance has addresses but no port. The query's address_type
// chooses which address and port or port_label supplies the port, exactly
// as for the aws provider's ec2 service. An instance that can yield
// neither is excluded and reported rather than failing the refresh: a
// compute inventory routinely contains hosts that are simply not labeled
// yet.
package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	gcpopts "github.com/trickstercache/trickster/v2/pkg/discovery/gcp/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/poller"
	pollerhttp "github.com/trickstercache/trickster/v2/pkg/discovery/poller/http"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// ErrStopped aliases discovery.ErrStopped for callers of this package
var ErrStopped = discovery.ErrStopped

const (
	// DefaultEndpoint is the Compute API's base URL
	DefaultEndpoint = "https://compute.googleapis.com"
	// maxResponseBytes bounds one API response page
	maxResponseBytes = 32 << 20
	// maxPages bounds pagination, so a pathological pageToken loop cannot
	// spin forever inside a single poll
	maxPages = 100
	// resolveTimeout bounds credential and project resolution, so a wedged
	// metadata server cannot hang a poll indefinitely
	resolveTimeout = 15 * time.Second
)

// provider carries the discoverer's connection-level settings; the shared
// discovery.Lifecycle owns Start/Stop/Subscribe
type provider struct {
	name     string
	endpoint string
	client   *http.Client
	gcp      *gcpopts.Options
	http     *do.HTTPOptions
	// pageSize caps instances per response page. Zero omits the parameter,
	// letting the API use its own default; tests lower it to force real
	// pagination.
	pageSize int

	// resolution of credentials and of the project is lazy and retried, so
	// that startup does not depend on the metadata server being reachable
	// at that instant and a momentary failure does not permanently disable
	// the provider. Only successes are cached.
	mtx     sync.Mutex
	tokens  oauth2.TokenSource
	project string
}

// New constructs the gcp Discoverer; it satisfies
// discovery.NewDiscovererFunc.
func New(name string, o *do.Options) (discovery.Discoverer, error) {
	p, err := newProvider(name, o)
	if err != nil {
		return nil, err
	}
	return discovery.NewLifecycle(name, p.newSubscription), nil
}

// newProvider builds the provider's connection state. It performs no
// network I/O.
func newProvider(name string, o *do.Options) (*provider, error) {
	if o == nil || o.GCP == nil {
		return nil, errors.New("gcp discovery requires a 'gcp' options block")
	}
	service := o.GCP.Service
	if service == "" {
		return nil, fmt.Errorf(
			"gcp discovery requires 'gcp.service'; supported services are %s",
			strings.Join(gcpopts.SupportedServices(), ", "))
	}
	if !slices.Contains(gcpopts.SupportedServices(), service) {
		return nil, fmt.Errorf(
			"gcp discovery service %q is not supported; supported services are %s",
			service, strings.Join(gcpopts.SupportedServices(), ", "))
	}
	httpOpts := o.HTTP
	if httpOpts == nil {
		httpOpts = &do.HTTPOptions{}
	}
	endpoint := DefaultEndpoint
	if httpOpts.Endpoint != "" {
		u, err := url.Parse(httpOpts.Endpoint)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("gcp endpoint %q must be an absolute url",
				httpOpts.Endpoint)
		}
		endpoint = strings.TrimSuffix(httpOpts.Endpoint, "/")
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
		Transport: pollerhttp.TransportOptions{
			MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2,
		},
	})
	if err != nil {
		return nil, err
	}
	return &provider{
		name: name, endpoint: endpoint, client: client,
		gcp: o.GCP, http: httpOpts,
	}, nil
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

// tokenSource resolves and caches the credential source.
func (p *provider) tokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	p.mtx.Lock()
	ts := p.tokens
	p.mtx.Unlock()
	if ts != nil {
		return ts, nil
	}

	rctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	creds, err := p.findCredentials(rctx)
	if err != nil {
		// deliberately not cached: a transient metadata-server failure must
		// not permanently disable discovery
		return nil, err
	}

	p.mtx.Lock()
	defer p.mtx.Unlock()
	if p.tokens == nil {
		p.tokens = creds.TokenSource
		if p.project == "" && creds.ProjectID != "" {
			// Application Default Credentials often carry the project, which
			// saves a metadata-server round trip
			p.project = creds.ProjectID
		}
	}
	return p.tokens, nil
}

// findCredentials resolves credentials from the configured key file, or
// from Application Default Credentials.
func (p *provider) findCredentials(ctx context.Context) (*google.Credentials, error) {
	if f := p.gcp.CredentialsFile; f != "" {
		b, err := os.ReadFile(f) // #nosec G304 -- operator-configured path
		if err != nil {
			return nil, fmt.Errorf("reading gcp credentials_file: %w", err)
		}
		// the credential type is pinned to service_account rather than
		// accepted from the file. An external_account or
		// impersonated_service_account configuration can name an arbitrary
		// token URL or local executable, so a file that turns out to be one
		// of those would hand credential resolution somewhere unintended.
		// Requiring the type closes that: for user credentials, use
		// Application Default Credentials instead of this field.
		creds, err := google.CredentialsFromJSONWithType(ctx, b,
			google.ServiceAccount, gcpopts.ComputeReadonlyScope)
		if err != nil {
			return nil, fmt.Errorf("parsing gcp credentials_file: %w", err)
		}
		return creds, nil
	}
	creds, err := google.FindDefaultCredentials(ctx, gcpopts.ComputeReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("resolving google application default credentials: %w", err)
	}
	return creds, nil
}

// projectID resolves and caches the project to list instances in.
func (p *provider) projectID(ctx context.Context) (string, error) {
	if p.gcp.Project != "" {
		return p.gcp.Project, nil
	}
	p.mtx.Lock()
	project := p.project
	p.mtx.Unlock()
	if project != "" {
		return project, nil
	}
	// resolving credentials may itself yield the project
	if _, err := p.tokenSource(ctx); err != nil {
		return "", err
	}
	p.mtx.Lock()
	project = p.project
	p.mtx.Unlock()
	if project == "" {
		return "", gcpopts.ErrMissingProject
	}
	return project, nil
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
	s := &subscription{
		p:      p,
		filter: q.Filter,
		mapping: mapping{
			scheme:            scheme,
			addressType:       q.GetAddressType(),
			port:              q.Port,
			portLabel:         q.PortLabel,
			replicaGroupLabel: q.ReplicaGroupLabel,
			tags:              q.Tags,
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

// subscription is one query's poll loop; it implements
// discovery.SubscriptionRunner and poller.Source
type subscription struct {
	p       *provider
	filter  string
	mapping mapping
	emitter *discovery.Emitter
	poller  *poller.Poller

	mtx           sync.Mutex
	stopped       bool
	failing       bool
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

// Poll implements poller.Source: one poll is every page of the aggregated
// instance list.
func (s *subscription) Poll(ctx context.Context) (time.Duration, error) {
	instances, err := s.listInstances(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return 0, err
		}
		s.warn(err)
		return 0, err
	}
	snap, skipped := toMembers(instances, s.mapping)
	s.reportSkipped(skipped)
	s.clearWarn()
	s.emitter.Emit(snap)
	return 0, nil // defer to the configured interval
}

// listInstances pages through instances.aggregatedList.
//
// Pages are accumulated and applied together: a partial page set is a
// partial membership, and emitting one would drain the pool of everything
// the later pages would have contained.
func (s *subscription) listInstances(ctx context.Context) ([]gceInstance, error) {
	project, err := s.p.projectID(ctx)
	if err != nil {
		return nil, err
	}
	var out []gceInstance
	var token string
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf(
				"instances.aggregatedList did not terminate after %d pages", maxPages)
		}
		resp, err := s.listPage(ctx, project, token)
		if err != nil {
			return nil, err
		}
		out = append(out, resp.instancesOf()...)
		if resp.NextPageToken == "" {
			return out, nil
		}
		token = resp.NextPageToken
	}
}

// listPage performs one authorized aggregatedList request and decodes it.
func (s *subscription) listPage(ctx context.Context,
	project, token string,
) (*aggregatedList, error) {
	payload, err := s.fetchPage(ctx, project, token)
	if err != nil {
		return nil, err
	}
	return parseAggregatedList(payload)
}

// fetchPage performs one authorized aggregatedList request and returns the
// undecoded body. It is separate from listPage so that a caller wanting the
// document as the API actually sent it -- capturing a test fixture, say --
// gets that rather than a round trip through this package's own types,
// which would only ever prove those types self-consistent.
func (s *subscription) fetchPage(ctx context.Context,
	project, token string,
) ([]byte, error) {
	u := s.p.endpoint + "/compute/v1/projects/" + url.PathEscape(project) +
		"/aggregated/instances"
	v := url.Values{}
	if s.filter != "" {
		v.Set("filter", s.filter)
	}
	if token != "" {
		v.Set("pageToken", token)
	}
	if s.p.pageSize > 0 {
		v.Set("maxResults", strconv.Itoa(s.p.pageSize))
	}
	// returnPartialSuccess keeps a single unreachable zone from failing the
	// whole call, which is what the API does otherwise; an unreachable zone
	// contributes no instances rather than no membership at all
	v.Set("returnPartialSuccess", "true")
	if q := v.Encode(); q != "" {
		u += "?" + q
	}

	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	ts, err := s.p.tokenSource(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := ts.Token()
	if err != nil {
		return nil, fmt.Errorf("obtaining google access token: %w", err)
	}
	tok.SetAuthHeader(r)

	resp, err := s.p.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := tbytes.ReadBoundedBody(resp.Body, maxResponseBytes, false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp.StatusCode, payload)
	}
	return payload, nil
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
	discovery.LogWarn("gcp discovery excluded instances that could not become members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Detail:     summary,
		})
}

// warn counts a refresh failure and logs it once per failure streak
func (s *subscription) warn(err error) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.GCP).Inc()
	s.mtx.Lock()
	failing := s.failing
	s.failing = true
	s.mtx.Unlock()
	if failing {
		return
	}
	discovery.LogWarn("gcp discovery refresh failed; keeping last-good members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
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
	discovery.LogInfo("gcp discovery refresh recovered",
		logging.Pairs{keys.Discoverer: s.p.name})
}

// onPanic reports a panicking poll as a refresh error, so a provider bug
// surfaces on the same metric and log stream as an upstream failure rather
// than silently freezing the membership.
func (s *subscription) onPanic(r any, stack []byte) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.GCP).Inc()
	discovery.LogError("panic during gcp discovery refresh; keeping last-good members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Panic:      fmt.Sprintf("%v", r),
			keys.Stack:      string(stack),
		})
}
