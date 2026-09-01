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

// Package azure implements the azure autodiscovery provider, reading
// members from an Azure Resource Manager API selected by
// discovery.<name>.azure.service.
//
// # One provider, several services
//
// 'service' is required and selects which API to read. Only 'vm' exists
// today -- Virtual Machines -- and the field is required all the same, so
// that later services are chosen rather than opted out of, matching 'aws'
// and 'gcp'.
//
// # No SDK
//
// The calls here are hand-written against an HTTP client. The Azure SDK
// (azidentity + armcompute + armnetwork) is deliberately not used: it is a
// very large dependency tree for three list calls and an OAuth2 grant.
// What it would genuinely buy is the credential matrix, and the three
// modes that matter -- client secret, workload identity, managed identity
// -- are each a form post; see token.go.
//
// # The join
//
// An Azure VM carries no address. Addresses live on network interfaces,
// which the VM references by resource id, and a public address is a
// further reference from the interface to a publicIPAddresses resource.
// One refresh is therefore two list calls, or three when address_type is
// public, joined in memory. Resource ids are case-insensitive and the
// casing genuinely differs between APIs, so the join folds case -- see
// resourceKey.
//
// # Hosts, not endpoints
//
// A VM has addresses but no port, so the query's port or port_label
// supplies one, exactly as for aws ec2 and gcp gce.
package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	azureopts "github.com/trickstercache/trickster/v2/pkg/discovery/azure/options"
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

// maxResponseBytes bounds one list page
const maxResponseBytes = 64 << 20 // 64 MiB

// apiVersionParam is the query parameter every ARM request must carry
const apiVersionParam = "api-version"

// maxPages bounds nextLink following, so a server that returns a cyclic
// or endless page chain fails the refresh rather than looping forever
const maxPages = 200

// provider carries the discoverer's connection state; the shared
// discovery.Lifecycle owns Start/Stop/Subscribe
type provider struct {
	name     string
	endpoint string
	client   *http.Client
	http     *do.HTTPOptions
	azure    *azureopts.Options
	tokens   *tokenSource
}

// New constructs the azure Discoverer; it satisfies
// discovery.NewDiscovererFunc.
func New(name string, o *do.Options) (discovery.Discoverer, error) {
	p, err := newProvider(name, o)
	if err != nil {
		return nil, err
	}
	return discovery.NewLifecycle(name, p.newSubscription), nil
}

// newProvider builds the provider's connection state. It performs no
// network I/O: a token is acquired lazily on the first poll, so startup
// does not depend on the metadata service being reachable at that instant.
func newProvider(name string, o *do.Options) (*provider, error) {
	if o == nil || o.Azure == nil {
		return nil, errors.New("azure discovery requires an 'azure' options block")
	}
	service := o.Azure.Service
	if service == "" {
		return nil, fmt.Errorf(
			"azure discovery requires 'azure.service'; supported services are %s",
			strings.Join(azureopts.SupportedServices(), ", "))
	}
	if !slices.Contains(azureopts.SupportedServices(), service) {
		return nil, fmt.Errorf(
			"azure discovery service %q is not supported; supported services are %s",
			service, strings.Join(azureopts.SupportedServices(), ", "))
	}
	if o.Azure.SubscriptionID == "" {
		return nil, azureopts.ErrMissingSubscription
	}
	httpOpts := o.HTTP
	if httpOpts == nil {
		httpOpts = &do.HTTPOptions{}
	}
	endpoint := o.Azure.ManagementEndpoint()
	if httpOpts.Endpoint != "" {
		u, err := url.Parse(httpOpts.Endpoint)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("azure endpoint %q must be an absolute url",
				httpOpts.Endpoint)
		}
		endpoint = strings.TrimSuffix(httpOpts.Endpoint, "/")
	}
	// a refresh makes several requests, so it drives the client directly
	// rather than through a one-request-per-iteration Source; the client
	// is still the shared one, so TLS and pooling behave as everywhere else
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
	p := &provider{
		name: name, endpoint: endpoint, client: client,
		http: httpOpts, azure: o.Azure,
	}
	p.tokens = newTokenSource(o.Azure, client)
	return p, nil
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

// scope returns the resource path prefix for the configured subscription,
// narrowed to a resource group when one is set.
func (p *provider) scope() string {
	s := "/subscriptions/" + url.PathEscape(p.azure.SubscriptionID)
	if g := p.azure.ResourceGroup; g != "" {
		s += "/resourceGroups/" + url.PathEscape(g)
	}
	return s
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
		p: p,
		mapping: mapping{
			scheme:            scheme,
			addressType:       q.AddressType,
			port:              q.Port,
			portLabel:         q.PortLabel,
			replicaGroupLabel: q.ReplicaGroupLabel,
			tags:              q.Tags,
			powerState:        p.azure.PowerState,
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
// discovery.SubscriptionRunner
type subscription struct {
	p       *provider
	mapping mapping
	emitter *discovery.Emitter
	poller  *poller.Poller

	mtx           sync.Mutex
	stopped       bool
	failing       bool
	skippedLogged string
	// registrationChecked bounds the unregistered-provider diagnostic to
	// one attempt, so an empty subscription does not pay for it every poll
	registrationChecked bool
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

// Poll performs one refresh and emits the resulting membership; it
// implements poller.Source. Every failure keeps the last-good membership.
func (s *subscription) Poll(ctx context.Context) (time.Duration, error) {
	inv, err := s.inventory(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// stopped mid-poll; not a refresh failure
			return 0, err
		}
		s.warn(err)
		return 0, err
	}
	s.clearWarn()
	snap, skipped := toMembers(inv, s.mapping)
	s.reportSkipped(skipped)
	s.emitter.Emit(snap)
	return 0, nil // defer to the configured interval
}

// inventory performs the calls one refresh needs and joins them.
//
// The public-address list is fetched only when the query asks for public
// addresses: it is a third round trip that most deployments never need.
func (s *subscription) inventory(ctx context.Context) (*inventory, error) {
	vms, err := listAll[virtualMachine](ctx, s.p, s.p.vmURL())
	if err != nil {
		return nil, fmt.Errorf("listing virtual machines: %w", err)
	}
	if len(vms) == 0 {
		s.checkRegistration(ctx)
	}
	nics, err := listAll[networkInterface](ctx, s.p, s.p.nicURL())
	if err != nil {
		return nil, fmt.Errorf("listing network interfaces: %w", err)
	}
	inv := &inventory{
		vms:       vms,
		nics:      make(map[string]*networkInterface, len(nics)),
		publicIPs: map[string]*publicIPAddress{},
		states:    map[string]string{},
	}
	for i := range nics {
		inv.nics[resourceKey(nics[i].ID)] = &nics[i]
	}
	if s.mapping.addressType == do.AddressPublic {
		pips, err := listAll[publicIPAddress](ctx, s.p, s.p.publicIPURL())
		if err != nil {
			return nil, fmt.Errorf("listing public ip addresses: %w", err)
		}
		for i := range pips {
			inv.publicIPs[resourceKey(pips[i].ID)] = &pips[i]
		}
	}
	if s.mapping.powerState {
		// statusOnly returns the instance view for every VM in the
		// subscription in one call, so power state costs one extra list
		// rather than one request per VM
		stated, err := listAll[virtualMachine](ctx, s.p, s.p.vmStatusURL())
		if err != nil {
			return nil, fmt.Errorf("listing virtual machine power states: %w", err)
		}
		for i := range stated {
			if st := powerStateOf(stated[i].Properties.InstanceView); st != "" {
				inv.states[resourceKey(stated[i].ID)] = st
			}
		}
		if len(vms) > 0 && len(inv.states) == 0 {
			// Without states every VM reads as not-running and would be
			// dropped, emptying the pool with no error and no exclusion
			// to explain it. That is the worst failure this provider
			// could have, so an unusable status list fails the refresh
			// instead: the last-good membership is kept and the operator
			// gets a message.
			return nil, errors.New(
				"power_state is enabled but azure returned no instance " +
					"view for any of the " + strconv.Itoa(len(vms)) +
					" virtual machines listed; refusing to treat every " +
					"member as stopped")
		}
	}
	return inv, nil
}

// vmURL builds the virtualMachines list URL for the configured scope.
func (p *provider) vmURL() string {
	v := url.Values{apiVersionParam: {p.azure.GetComputeAPIVersion()}}
	return p.endpoint + p.scope() +
		"/providers/Microsoft.Compute/virtualMachines?" + v.Encode()
}

// vmStatusURL builds the list URL that carries every VM's instance view.
//
// It is deliberately **subscription-scoped even when resource_group is
// set**. statusOnly is a parameter of the subscription-wide list only;
// the resource-group-scoped list accepts it, returns HTTP 200, and
// silently ignores it -- confirmed against a live subscription. Scoping
// this call to the group would therefore yield no statuses at all, and
// every VM would read as stopped.
//
// The cost is that power_state needs read permission at subscription
// scope. Statuses for VMs outside the configured group simply never
// match anything in the join.
func (p *provider) vmStatusURL() string {
	v := url.Values{
		apiVersionParam: {p.azure.GetComputeAPIVersion()},
		"statusOnly":    {"true"},
	}
	return p.endpoint + "/subscriptions/" +
		url.PathEscape(p.azure.SubscriptionID) +
		"/providers/Microsoft.Compute/virtualMachines?" + v.Encode()
}

// nicURL builds the networkInterfaces list URL
func (p *provider) nicURL() string {
	v := url.Values{apiVersionParam: {p.azure.GetNetworkAPIVersion()}}
	return p.endpoint + p.scope() +
		"/providers/Microsoft.Network/networkInterfaces?" + v.Encode()
}

// publicIPURL builds the publicIPAddresses list URL
func (p *provider) publicIPURL() string {
	v := url.Values{apiVersionParam: {p.azure.GetNetworkAPIVersion()}}
	return p.endpoint + p.scope() +
		"/providers/Microsoft.Network/publicIPAddresses?" + v.Encode()
}

// listAll follows nextLink to the end of a paged ARM list.
func listAll[T any](ctx context.Context, p *provider, first string) ([]T, error) {
	var out []T
	next := first
	for pages := 0; next != ""; pages++ {
		if pages >= maxPages {
			return nil, fmt.Errorf(
				"azure list did not terminate after %d pages", maxPages)
		}
		page, err := getPage[T](ctx, p, next)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Value...)
		// nextLink is a complete URL, already carrying api-version and a
		// skip token, so it is followed as given rather than rebuilt
		next = page.NextLink
	}
	return out, nil
}

// getPage performs one authorized list request and decodes it
func getPage[T any](ctx context.Context, p *provider, target string) (*listResponse[T], error) {
	body, err := p.get(ctx, target)
	if err != nil {
		return nil, err
	}
	return parseList[T](body)
}

// get performs one authorized GET and returns the response body
func (p *provider) get(ctx context.Context, target string) ([]byte, error) {
	token, err := p.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	return tbytes.ReadBoundedBody(resp.Body, maxResponseBytes, false)
}

// armError is the ARM error document
type armError struct {
	Error armErrorDetail `json:"error"`
}

// armErrorDetail is the body of an ARM error
type armErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// maxErrorBytes bounds an error document read
const maxErrorBytes = 64 << 10

// checkStatus converts a non-2xx response into an error carrying whatever
// ARM said went wrong, which is how a missing role assignment or an
// unsupported api-version surfaces legibly.
func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := tbytes.ReadBoundedBody(resp.Body, maxErrorBytes, false)
	var e armError
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		if e.Error.Code != "" {
			return fmt.Errorf("azure api error (http %d): %s: %s",
				resp.StatusCode, e.Error.Code, e.Error.Message)
		}
		return fmt.Errorf("azure api error (http %d): %s",
			resp.StatusCode, e.Error.Message)
	}
	return fmt.Errorf("azure api returned http %d", resp.StatusCode)
}

// computeProviderPath is the resource-provider registration record for
// Microsoft.Compute
const computeProviderPath = "/providers/Microsoft.Compute"

// providerRegistrationAPIVersion is the api-version for the resource
// provider registration record
const providerRegistrationAPIVersion = "2021-04-01"

// registeredState is the registrationState of a usable resource provider
const registeredState = "Registered"

// resourceProvider is the subscription's registration record for one
// resource provider
type resourceProvider struct {
	RegistrationState string `json:"registrationState"`
}

// checkRegistration explains an empty VM list when the cause is an
// unregistered resource provider.
//
// This exists because of how ARM answers in that case: a subscription
// where Microsoft.Compute is not registered returns **HTTP 200 with an
// empty value array**, not an error. Membership is then legitimately
// empty as far as this provider can tell, so the refresh cannot fail --
// but the operator sees a silently empty pool with no metric and no log,
// and the cause is not guessable. New subscriptions start unregistered,
// so this is a first-run experience rather than an exotic case.
//
// It runs only when the list came back empty, and logs at most once, so
// a subscription that genuinely holds no matching VMs pays one extra
// request and nothing more.
func (s *subscription) checkRegistration(ctx context.Context) {
	s.mtx.Lock()
	if s.registrationChecked {
		s.mtx.Unlock()
		return
	}
	s.registrationChecked = true
	s.mtx.Unlock()

	target := s.p.endpoint + "/subscriptions/" +
		url.PathEscape(s.p.azure.SubscriptionID) + computeProviderPath +
		"?" + url.Values{apiVersionParam: {providerRegistrationAPIVersion}}.Encode()
	body, err := s.p.get(ctx, target)
	if err != nil {
		// the diagnostic is best-effort; its failure must not become the
		// operator's problem on top of the empty list
		return
	}
	var rp resourceProvider
	if json.Unmarshal(body, &rp) != nil || rp.RegistrationState == "" {
		return
	}
	if rp.RegistrationState == registeredState {
		return
	}
	discovery.LogWarn(
		"azure discovery found no virtual machines because the "+
			"Microsoft.Compute resource provider is not registered on this "+
			"subscription; azure returns an empty list rather than an error "+
			"for this, so the pool is silently empty",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Detail: "run: az provider register -n Microsoft.Compute " +
				"(and Microsoft.Network)",
			"registrationState": rp.RegistrationState,
		})
}

// reportSkipped logs VMs that could not become members, once per distinct
// set rather than every poll
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
	discovery.LogWarn("azure discovery excluded vms that could not become members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.Detail:     detail,
		})
}

// warn counts a refresh failure and logs it once per failure streak
func (s *subscription) warn(err error) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.Azure).Inc()
	s.mtx.Lock()
	failing := s.failing
	s.failing = true
	s.mtx.Unlock()
	if failing {
		return
	}
	discovery.LogWarn("azure discovery refresh failed; keeping last-good members",
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
	discovery.LogInfo("azure discovery refresh recovered",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.URL:        discovery.SanitizeURL(s.p.endpoint),
		})
}

// onPanic reports a panicking poll as a refresh error, so a provider bug
// surfaces on the same metric and log stream as an upstream failure
// rather than silently freezing the membership.
func (s *subscription) onPanic(r any, stack []byte) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.Azure).Inc()
	discovery.LogError("panic during azure discovery refresh; keeping last-good members",
		logging.Pairs{
			keys.Discoverer: s.p.name,
			keys.URL:        discovery.SanitizeURL(s.p.endpoint),
			keys.Panic:      fmt.Sprintf("%v", r),
			keys.Stack:      string(stack),
		})
}
