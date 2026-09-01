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

package azure

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	azureopts "github.com/trickstercache/trickster/v2/pkg/discovery/azure/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// fakeARM serves canned responses keyed by resource type, and records the
// paths it was asked for.
type fakeARM struct {
	*httptest.Server
	mtx      sync.Mutex
	paths    []string
	handlers map[string]func(r *http.Request) (int, string)
}

func newFakeARM(t *testing.T) *fakeARM {
	f := &fakeARM{handlers: map[string]func(*http.Request) (int, string){}}
	f.Server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			f.mtx.Lock()
			f.paths = append(f.paths, r.URL.Path+"?"+r.URL.RawQuery)
			f.mtx.Unlock()
			for suffix, h := range f.handlers {
				if strings.Contains(r.URL.Path, suffix) {
					code, body := h(r)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(code)
					_, _ = w.Write([]byte(body))
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NotFound","message":"no handler"}}`))
		}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeARM) on(suffix string, body string) {
	f.handlers[suffix] = func(*http.Request) (int, string) { return 200, body }
}

func (f *fakeARM) onFunc(suffix string, h func(*http.Request) (int, string)) {
	f.handlers[suffix] = h
}

func (f *fakeARM) requested() []string {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *fakeARM) askedFor(fragment string) bool {
	for _, p := range f.requested() {
		if strings.Contains(p, fragment) {
			return true
		}
	}
	return false
}

func fakeOptions(endpoint string) *do.Options {
	return &do.Options{
		Name:     "test-azure",
		Provider: "azure",
		Azure: &azureopts.Options{
			Service:        azureopts.ServiceVM,
			SubscriptionID: subID,
		},
		HTTP: &do.HTTPOptions{Endpoint: endpoint},
	}
}

// testProvider builds a provider with a pre-cached token, so tests
// exercise the ARM calls rather than the credential flow (which token_test
// covers).
func testProvider(t *testing.T, o *do.Options) *provider {
	t.Helper()
	p, err := newProvider("test-azure", o)
	require.NoError(t, err)
	p.tokens.token = "test-token"
	p.tokens.expires = time.Now().Add(time.Hour)
	return p
}

func vmListBody(names ...string) string {
	var sb strings.Builder
	sb.WriteString(`{"value":[`)
	for i, n := range names {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":%q,"name":%q,"location":"eastus",
		  "tags":{"port":"9090"},
		  "properties":{"vmId":"v-%s","networkProfile":{"networkInterfaces":[
		    {"id":%q}]}}}`, vmID(n), n, n, nicID)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

func nicListBody() string {
	return fmt.Sprintf(`{"value":[{"id":%q,"name":"nic-1","properties":{
	  "ipConfigurations":[{"properties":{"primary":true,
	    "privateIPAddress":"10.0.0.4"}}]}}]}`, nicID)
}

func TestNewRequiresAzureOptions(t *testing.T) {
	_, err := New("test", nil)
	require.Error(t, err)
	_, err = New("test", &do.Options{Provider: "azure"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "'azure' options block")
}

// service is required even though 'vm' is the only value this build
// supports, matching aws.service and gcp.service: a default could never be
// taken back once configs rely on it.
func TestNewRequiresService(t *testing.T) {
	o := fakeOptions("https://example.com")
	o.Azure.Service = ""
	_, err := New("test", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'azure.service'")

	o = fakeOptions("https://example.com")
	o.Azure.Service = "virtualmachines"
	_, err = New("test", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")

	o = fakeOptions("https://example.com")
	o.Azure.SubscriptionID = ""
	_, err = New("test", o)
	require.ErrorIs(t, err, azureopts.ErrMissingSubscription)
}

// The endpoint comes from the configured cloud, so a sovereign-cloud
// deployment needs no URL in config.
func TestEndpointComesFromTheCloud(t *testing.T) {
	o := fakeOptions("")
	p, err := newProvider("test", o)
	require.NoError(t, err)
	require.Equal(t, "https://management.azure.com", p.endpoint)

	o = fakeOptions("")
	o.Azure.Cloud = azureopts.CloudUSGovernment
	p, err = newProvider("test", o)
	require.NoError(t, err)
	require.Equal(t, "https://management.usgovcloudapi.net", p.endpoint)

	o = fakeOptions("not-absolute")
	_, err = newProvider("test", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be an absolute url")
}

func TestRequestShape(t *testing.T) {
	f := newFakeARM(t)
	f.on("virtualMachines", vmListBody("web-1"))
	f.on("networkInterfaces", nicListBody())

	p := testProvider(t, fakeOptions(f.URL))
	s := &subscription{p: p, mapping: baseMapping(),
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
	_, err := s.inventory(t.Context())
	require.NoError(t, err)

	require.True(t, f.askedFor("/subscriptions/"+subID+
		"/providers/Microsoft.Compute/virtualMachines"))
	require.True(t, f.askedFor("api-version="+azureopts.DefaultComputeAPIVersion))
	require.True(t, f.askedFor("api-version="+azureopts.DefaultNetworkAPIVersion))
	require.False(t, f.askedFor("publicIPAddresses"),
		"the public ip list is a third round trip most deployments never need")
	require.False(t, f.askedFor("statusOnly"))
}

// A resource group narrows the scope of every list, so a subscription
// with thousands of machines is not enumerated to find a handful.
func TestResourceGroupNarrowsTheScope(t *testing.T) {
	o := fakeOptions("https://example.com")
	o.Azure.ResourceGroup = "prod-rg"
	p, err := newProvider("test", o)
	require.NoError(t, err)
	require.Contains(t, p.vmURL(), "/resourceGroups/prod-rg/providers/")
	require.Contains(t, p.nicURL(), "/resourceGroups/prod-rg/providers/")
}

// address_type: public is what makes the third call worth its cost.
func TestPublicAddressTypeFetchesThePublicIPList(t *testing.T) {
	f := newFakeARM(t)
	f.on("virtualMachines", vmListBody("web-1"))
	f.on("networkInterfaces", fmt.Sprintf(`{"value":[{"id":%q,"properties":{
	  "ipConfigurations":[{"properties":{"primary":true,
	    "privateIPAddress":"10.0.0.4",
	    "publicIPAddress":{"id":%q}}}]}}]}`, nicID, pipID))
	f.on("publicIPAddresses", fmt.Sprintf(
		`{"value":[{"id":%q,"properties":{"ipAddress":"20.1.2.3"}}]}`, pipID))

	p := testProvider(t, fakeOptions(f.URL))
	m := baseMapping()
	m.addressType = do.AddressPublic
	s := &subscription{p: p, mapping: m,
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
	i, err := s.inventory(t.Context())
	require.NoError(t, err)
	require.True(t, f.askedFor("publicIPAddresses"))

	snap, skipped := toMembers(i, m)
	require.Empty(t, skipped)
	require.Equal(t, "20.1.2.3:9090", snap[0].Address)
}

// Power state costs one extra list, not one request per VM: statusOnly
// returns the instance view for the whole subscription at once. That is
// what makes it affordable enough to offer at all.
func TestPowerStateIsOneExtraListNotOnePerVM(t *testing.T) {
	f := newFakeARM(t)
	f.onFunc("virtualMachines", func(r *http.Request) (int, string) {
		if r.URL.Query().Get("statusOnly") == "true" {
			// the status list must be subscription-scoped: the
			// resource-group-scoped list accepts statusOnly, returns 200,
			// and silently ignores it
			if strings.Contains(r.URL.Path, "/resourceGroups/") {
				return 500, `{"error":{"message":"status list must not be group-scoped"}}`
			}
			return 200, fmt.Sprintf(`{"value":[{"id":%q,"name":"web-1",
			  "properties":{"instanceView":{"statuses":[
			    {"code":"ProvisioningState/succeeded"},
			    {"code":"PowerState/running"}]}}}]}`, vmID("web-1"))
		}
		return 200, vmListBody("web-1")
	})
	f.on("networkInterfaces", nicListBody())

	o := fakeOptions(f.URL)
	o.Azure.PowerState = true
	p := testProvider(t, o)
	m := baseMapping()
	m.powerState = true
	s := &subscription{p: p, mapping: m,
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
	i, err := s.inventory(t.Context())
	require.NoError(t, err)
	require.True(t, f.askedFor("statusOnly=true"))
	require.Equal(t, powerStateRunning, i.states[resourceKey(vmID("web-1"))])

	snap, _ := toMembers(i, m)
	require.Len(t, snap, 1)
	require.Equal(t, discovery.Ready, snap[0].Ready)

	// three requests total: vms, nics, statuses -- not one per machine
	require.Len(t, f.requested(), 3)
}

// statusOnly is a parameter of the subscription-wide list only. The
// resource-group-scoped list accepts it, returns 200, and ignores it --
// confirmed live. Scoping the status call to the group would yield no
// statuses, and every VM would read as stopped.
func TestStatusListIsSubscriptionScopedEvenWithAResourceGroup(t *testing.T) {
	o := fakeOptions("https://example.com")
	o.Azure.ResourceGroup = "prod-rg"
	p, err := newProvider("test", o)
	require.NoError(t, err)

	require.Contains(t, p.vmURL(), "/resourceGroups/prod-rg/providers/")
	require.NotContains(t, p.vmStatusURL(), "/resourceGroups/",
		"the status list must stay subscription-scoped")
	require.Contains(t, p.vmStatusURL(), "statusOnly=true")
}

// Without statuses every VM reads as not-running and would be dropped,
// emptying the pool with no error and nothing to explain it. That is the
// worst failure this provider could have, so it fails the refresh
// instead and keeps the last-good membership.
func TestUnusableStatusListFailsRatherThanEmptyingThePool(t *testing.T) {
	f := newFakeARM(t)
	f.onFunc("virtualMachines", func(r *http.Request) (int, string) {
		if r.URL.Query().Get("statusOnly") == "true" {
			// VMs returned, but with no instance view -- exactly what the
			// group-scoped list does
			return 200, vmListBody("web-1")
		}
		return 200, vmListBody("web-1")
	})
	f.on("networkInterfaces", nicListBody())

	o := fakeOptions(f.URL)
	o.Azure.PowerState = true
	p := testProvider(t, o)
	m := baseMapping()
	m.powerState = true
	s := &subscription{p: p, mapping: m,
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}

	_, err := s.inventory(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to treat every member as stopped")
}

// An empty subscription with power_state on is not the broken case; the
// guard must not fire when there are simply no VMs.
func TestNoVMsWithPowerStateIsNotAnError(t *testing.T) {
	f := newFakeARM(t)
	f.on("virtualMachines", `{"value":[]}`)
	f.on("networkInterfaces", `{"value":[]}`)
	f.on("/providers/Microsoft.Compute?", `{"registrationState":"Registered"}`)

	o := fakeOptions(f.URL)
	o.Azure.PowerState = true
	p := testProvider(t, o)
	m := baseMapping()
	m.powerState = true
	s := &subscription{p: p, mapping: m,
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
	_, err := s.inventory(t.Context())
	require.NoError(t, err)
}

// nextLink is a complete URL carrying its own api-version and skip token,
// so it is followed as given rather than rebuilt.
func TestPaginationFollowsNextLink(t *testing.T) {
	f := newFakeARM(t)
	var page int
	f.onFunc("virtualMachines", func(r *http.Request) (int, string) {
		page++
		if page == 1 {
			return 200, fmt.Sprintf(`{"value":[{"id":%q,"name":"web-1",
			  "tags":{"port":"9090"},
			  "properties":{"networkProfile":{"networkInterfaces":[{"id":%q}]}}}],
			  "nextLink":%q}`, vmID("web-1"), nicID,
				f.URL+"/next/virtualMachines?token=abc")
		}
		return 200, vmListBody("web-2")
	})
	f.on("networkInterfaces", nicListBody())

	p := testProvider(t, fakeOptions(f.URL))
	s := &subscription{p: p, mapping: baseMapping(),
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
	i, err := s.inventory(t.Context())
	require.NoError(t, err)
	require.Len(t, i.vms, 2, "both pages accumulate")
	require.True(t, f.askedFor("token=abc"))
}

// A server returning an endless page chain must fail the refresh rather
// than loop forever.
func TestPaginationIsBounded(t *testing.T) {
	f := newFakeARM(t)
	f.onFunc("virtualMachines", func(*http.Request) (int, string) {
		return 200, fmt.Sprintf(`{"value":[],"nextLink":%q}`,
			f.URL+"/loop/virtualMachines")
	})
	f.on("networkInterfaces", nicListBody())

	p := testProvider(t, fakeOptions(f.URL))
	s := &subscription{p: p, mapping: baseMapping(),
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
	_, err := s.inventory(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not terminate")
}

// ARM explains itself in the body; a missing role assignment is the most
// common failure and its message names exactly what is missing.
func TestARMErrorCarriesCodeAndMessage(t *testing.T) {
	f := newFakeARM(t)
	f.onFunc("virtualMachines", func(*http.Request) (int, string) {
		// one line: a raw newline inside a JSON string value is invalid,
		// and would exercise the fallback rather than the message path
		return http.StatusForbidden, `{"error":{"code":"AuthorizationFailed",` +
			`"message":"The client does not have authorization to perform ` +
			`action 'Microsoft.Compute/virtualMachines/read' over scope."}}`
	})
	p := testProvider(t, fakeOptions(f.URL))
	s := &subscription{p: p, mapping: baseMapping(),
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
	_, err := s.inventory(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "AuthorizationFailed")
	require.Contains(t, err.Error(), "virtualMachines/read")
}

func TestCheckStatusFallsBackToTheStatusCode(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       http.NoBody,
	}
	err := checkStatus(resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
	require.NoError(t, checkStatus(&http.Response{StatusCode: 200}))
}

// ARM answers a list against an unregistered resource provider with HTTP
// 200 and an empty value array, not an error -- confirmed against a real
// subscription. Membership is then legitimately empty as far as this
// provider can tell, so the refresh cannot fail; what it must not do is
// leave the operator with a silently empty pool and no way to guess why.
// New subscriptions start unregistered, so this is a first-run case.
func TestEmptyListExplainsAnUnregisteredProvider(t *testing.T) {
	f := newFakeARM(t)
	f.on("virtualMachines", `{"value":[]}`)
	f.on("networkInterfaces", `{"value":[]}`)
	f.on("/providers/Microsoft.Compute?", `{"registrationState":"NotRegistered"}`)

	p := testProvider(t, fakeOptions(f.URL))
	s := &subscription{p: p, mapping: baseMapping(),
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
	inv, err := s.inventory(t.Context())
	require.NoError(t, err, "an empty list is not a refresh failure")
	require.Empty(t, inv.vms)
	require.True(t, f.askedFor("/providers/Microsoft.Compute?"))

	// at most once: an empty subscription must not pay for the diagnostic
	// on every poll forever
	before := len(f.requested())
	_, err = s.inventory(t.Context())
	require.NoError(t, err)
	after := len(f.requested())
	require.Equal(t, 2, after-before,
		"only the two list calls on the second refresh")
}

// A subscription that genuinely holds no matching VMs is not a
// misconfiguration; the diagnostic must stay quiet about it.
func TestEmptyListWithRegisteredProviderIsQuiet(t *testing.T) {
	f := newFakeARM(t)
	f.on("virtualMachines", `{"value":[]}`)
	f.on("networkInterfaces", `{"value":[]}`)
	f.on("/providers/Microsoft.Compute?", `{"registrationState":"Registered"}`)

	p := testProvider(t, fakeOptions(f.URL))
	s := &subscription{p: p, mapping: baseMapping(),
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
	_, err := s.inventory(t.Context())
	require.NoError(t, err)
}

// The diagnostic is best-effort: its own failure must not become the
// operator's problem on top of the empty list.
func TestRegistrationCheckFailureIsSwallowed(t *testing.T) {
	f := newFakeARM(t)
	f.on("virtualMachines", `{"value":[]}`)
	f.on("networkInterfaces", `{"value":[]}`)
	// no handler for the provider path: the fake answers 404

	p := testProvider(t, fakeOptions(f.URL))
	s := &subscription{p: p, mapping: baseMapping(),
		emitter: discovery.NewEmitter(func(discovery.Snapshot) {})}
	_, err := s.inventory(t.Context())
	require.NoError(t, err)
}

func TestPollEmitsMembers(t *testing.T) {
	f := newFakeARM(t)
	f.on("virtualMachines", vmListBody("web-1"))
	f.on("networkInterfaces", nicListBody())

	snaps := make(chan discovery.Snapshot, 4)
	p := testProvider(t, fakeOptions(f.URL))
	run, err := p.newSubscription(&do.Query{Port: "9090"},
		func(s discovery.Snapshot) { snaps <- s })
	require.NoError(t, err)
	run.Launch(t.Context())
	defer run.Stop()

	select {
	case snap := <-snaps:
		require.Len(t, snap, 1)
		require.Equal(t, "10.0.0.4:9090", snap[0].Address)
		require.Equal(t, "http", snap[0].Scheme)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a snapshot")
	}
}

// A refresh failure keeps the last-good membership rather than emitting
// an empty snapshot, which would drain the pool on a blip.
func TestRefreshFailureEmitsNothingAndCounts(t *testing.T) {
	before := counterValue(t, "test-azure")
	snaps := make(chan discovery.Snapshot, 4)
	p := testProvider(t, fakeOptions("http://127.0.0.1:1"))
	run, err := p.newSubscription(&do.Query{Port: "9090"},
		func(s discovery.Snapshot) { snaps <- s })
	require.NoError(t, err)
	run.Launch(t.Context())
	defer run.Stop()

	require.Eventually(t, func() bool {
		return counterValue(t, "test-azure") > before
	}, 5*time.Second, 20*time.Millisecond)
	select {
	case s := <-snaps:
		t.Fatalf("emitted %v on a refresh failure", s)
	default:
	}
}

func TestStopIsIdempotentAndLaunchAfterStopDoesNothing(t *testing.T) {
	f := newFakeARM(t)
	f.on("virtualMachines", vmListBody("web-1"))
	f.on("networkInterfaces", nicListBody())
	p := testProvider(t, fakeOptions(f.URL))
	run, err := p.newSubscription(&do.Query{Port: "9090"}, func(discovery.Snapshot) {})
	require.NoError(t, err)
	run.Stop()
	run.Stop()
	run.Launch(context.Background())
}

func counterValue(t *testing.T, name string) float64 {
	t.Helper()
	c, err := metrics.DiscoveryRefreshErrors.GetMetricWithLabelValues(name, "azure")
	require.NoError(t, err)
	var m dto.Metric
	require.NoError(t, c.(prometheus.Metric).Write(&m))
	return m.GetCounter().GetValue()
}
