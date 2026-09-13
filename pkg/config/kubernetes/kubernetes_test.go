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

package kubernetes

import (
	"testing"
	"time"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// valid returns an initialized section that passes validation, for tests
// that then break exactly one thing
func valid(t *testing.T) *Options {
	t.Helper()
	o := New()
	o.Defaults.RoutingMode = RoutingModeService
	require.NoError(t, o.Validate())
	return o
}

// An empty section is enough to run the controller, except for the routing
// mode, which has no safe default
func TestNewDefaults(t *testing.T) {
	o := New()
	require.True(t, o.IsEnabled())
	require.True(t, o.WritesCluster())
	require.True(t, o.ElectsLeader())
	require.Equal(t, DefaultGatewayClassControllerName, o.GatewayClassControllerName)
	require.Equal(t, timeconv.Duration(DefaultResyncInterval), o.ResyncInterval)
	require.Equal(t, timeconv.Duration(DefaultDebounceWindow), o.DebounceWindow)
	require.Equal(t, ao.HealthModeProvider, o.Defaults.HealthMode)
	require.Empty(t, o.RoutingMode())
	require.ErrorIs(t, o.Validate(), ErrRoutingModeRequired)
}

// The controller does not exist without the section, and a nil section must
// answer every question as "off" rather than panicking
func TestNilSectionIsDisabled(t *testing.T) {
	var o *Options
	require.False(t, o.IsEnabled())
	require.False(t, o.WritesCluster())
	require.False(t, o.ElectsLeader())
	require.Empty(t, o.RoutingMode())
	require.NoError(t, o.Validate())
	require.Nil(t, o.Clone())
	require.NotPanics(t, func() { o.Initialize() })
}

// Turning the controller off must not require the rest of the block to stay
// valid; an operator disabling it is not asked to fix configuration that no
// longer runs
func TestDisabledSectionSkipsValidation(t *testing.T) {
	o := New()
	o.Enabled = new(false)
	o.Defaults.RoutingMode = "nonsense"
	require.False(t, o.IsEnabled())
	require.NoError(t, o.Validate())
}

func TestInitializeAppliesDefaults(t *testing.T) {
	o := &Options{}
	o.Initialize()
	require.True(t, o.IsEnabled())
	require.NotNil(t, o.Connection)
	require.True(t, o.Connection.InCluster)
	require.Equal(t, DefaultGatewayClassControllerName, o.GatewayClassControllerName)
	require.Equal(t, DefaultLeaseName, o.LeaderElection.Name)
	require.Equal(t, ao.HealthModeProvider, o.Defaults.HealthMode)

	// an explicit value survives initialization
	e := &Options{
		GatewayClassControllerName: "example.com/gw",
		ResyncInterval:             timeconv.Duration(time.Minute),
		Defaults:                   &DefaultsOptions{HealthMode: ao.HealthModeProbe},
	}
	e.Initialize()
	require.Equal(t, "example.com/gw", e.GatewayClassControllerName)
	require.Equal(t, timeconv.Duration(time.Minute), e.ResyncInterval)
	require.Equal(t, ao.HealthModeProbe, e.Defaults.HealthMode)
}

// The routing mode is the one setting with no default: the two modes have
// different failure modes and guessing would make a misconfiguration look
// like a routing bug
func TestValidateRoutingMode(t *testing.T) {
	o := New()
	require.ErrorIs(t, o.Validate(), ErrRoutingModeRequired)

	o.Defaults = nil
	require.ErrorIs(t, o.Validate(), ErrRoutingModeRequired,
		"an absent defaults block is still a missing routing mode")

	for _, mode := range []string{RoutingModeService, RoutingModeEndpoint} {
		m := New()
		m.Defaults.RoutingMode = mode
		require.NoError(t, m.Validate())
		require.Equal(t, mode, m.RoutingMode())
	}

	bad := New()
	bad.Defaults.RoutingMode = "cluster"
	require.ErrorIs(t, bad.Validate(), ErrInvalidRoutingMode)
	require.ErrorContains(t, bad.Validate(), `"cluster"`)
}

func TestValidateHealthMode(t *testing.T) {
	for _, mode := range []string{"", ao.HealthModeProbe, ao.HealthModeProvider} {
		o := valid(t)
		o.Defaults.HealthMode = mode
		require.NoError(t, o.Validate())
	}
	o := valid(t)
	o.Defaults.HealthMode = "guess"
	require.ErrorIs(t, o.Validate(), ErrInvalidHealthMode)
}

// The two namespace-scoping mechanisms answer the same question; both set
// is ambiguous rather than additive
func TestValidateNamespaceScope(t *testing.T) {
	o := valid(t)
	o.WatchNamespaces = []string{"a"}
	require.NoError(t, o.Validate())

	sel := valid(t)
	sel.NamespaceSelector = map[string]string{"team": "infra"}
	require.NoError(t, sel.Validate())

	both := valid(t)
	both.WatchNamespaces = []string{"a"}
	both.NamespaceSelector = map[string]string{"team": "infra"}
	require.ErrorIs(t, both.Validate(), ErrNamespaceScopeConflict)
}

func TestValidateMisc(t *testing.T) {
	blank := valid(t)
	blank.GatewayClassControllerName = ""
	require.ErrorIs(t, blank.Validate(), ErrControllerNameRequired)

	neg := valid(t)
	neg.ResyncInterval = -1
	require.ErrorIs(t, neg.Validate(), ErrNegativeInterval)

	negDebounce := valid(t)
	negDebounce.DebounceWindow = -1
	require.ErrorIs(t, negDebounce.Validate(), ErrNegativeInterval)

	negTimeout := valid(t)
	negTimeout.Defaults.Timeout = -1
	require.ErrorIs(t, negTimeout.Validate(), ErrNegativeInterval)

	conn := valid(t)
	conn.Connection.InCluster = true
	conn.Connection.Kubeconfig = "/path"
	require.ErrorContains(t, conn.Validate(), "connection")
}

// A published service with only half a reference publishes nothing, so it
// is a configuration error rather than a silent no-op
func TestValidatePublishedService(t *testing.T) {
	o := valid(t)
	o.PublishedService = &ServiceRef{Namespace: "trickster", Name: "gateway"}
	require.NoError(t, o.Validate())
	require.Equal(t, "trickster/gateway", o.PublishedService.String())

	for _, bad := range []*ServiceRef{{Namespace: "trickster"}, {Name: "gateway"}} {
		p := valid(t)
		p.PublishedService = bad
		require.ErrorIs(t, p.Validate(), ErrIncompletePublishedService)
	}
	require.Empty(t, (*ServiceRef)(nil).String())
}

// Timings that cannot renew before the lease expires produce leadership that
// flaps rather than one that holds
func TestValidateLeaderElectionTimings(t *testing.T) {
	o := valid(t)
	require.NoError(t, o.Validate(), "the defaults must be a coherent set")

	late := valid(t)
	late.LeaderElection.RenewDeadline = late.LeaderElection.LeaseDuration
	require.ErrorIs(t, late.Validate(), ErrRenewDeadlineTooLong)

	slow := valid(t)
	slow.LeaderElection.RetryPeriod = slow.LeaderElection.RenewDeadline
	require.ErrorIs(t, slow.Validate(), ErrRetryPeriodTooLong)

	neg := valid(t)
	neg.LeaderElection.LeaseDuration = -1
	require.ErrorIs(t, neg.Validate(), ErrNegativeInterval)

	off := valid(t)
	off.LeaderElection.Enabled = new(false)
	require.False(t, off.ElectsLeader())
	require.NoError(t, off.Validate())
}

// Read-only is about the cluster, not the controller: it still runs, and it
// stops writing status, Events and the leader election Lease together,
// because electing a leader decides which replica does the writing
func TestReadOnly(t *testing.T) {
	o := valid(t)
	o.ReadOnly = true
	require.NoError(t, o.Validate())
	require.False(t, o.WritesCluster())
	require.False(t, o.ElectsLeader(),
		"a read-only instance must not contend for a lease it has no use for")
	require.True(t, o.IsEnabled(), "read-only is not off")

	// the addresses a published service supplies are only ever written into
	// status, so naming one is a contradiction rather than a no-op
	o.PublishedService = &ServiceRef{Namespace: "trickster", Name: "gateway"}
	require.ErrorIs(t, o.Validate(), ErrPublishedServiceReadOnly)

	// and writing is the default
	require.True(t, valid(t).WritesCluster())
}

func TestCloneIsIndependent(t *testing.T) {
	o := valid(t)
	o.WatchNamespaces = []string{"a", "b"}
	o.NamespaceSelector = map[string]string{"team": "infra"}
	o.PublishedService = &ServiceRef{Namespace: "n", Name: "s"}
	o.Defaults.CacheName = "default"

	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.WatchNamespaces[0] = "changed"
	c.NamespaceSelector["team"] = "changed"
	c.PublishedService.Name = "changed"
	c.Defaults.CacheName = "changed"
	*c.Enabled = false
	c.ReadOnly = true
	*c.LeaderElection.Enabled = false
	c.Connection.Kubeconfig = "/changed"

	require.Equal(t, "a", o.WatchNamespaces[0])
	require.Equal(t, "infra", o.NamespaceSelector["team"])
	require.Equal(t, "s", o.PublishedService.Name)
	require.Equal(t, "default", o.Defaults.CacheName)
	require.True(t, o.IsEnabled())
	require.True(t, o.WritesCluster())
	require.True(t, o.ElectsLeader())
	require.Empty(t, o.Connection.Kubeconfig)
}

// Every setting must survive a YAML round trip, and an untouched section
// must not gain fields the operator did not write
func TestYAMLRoundTrip(t *testing.T) {
	const src = `
enabled: true
connection:
  kubeconfig: /etc/kube/config
gateway_class_controller_name: example.com/gw
ingress_class: trickster
watch_namespaces: [team-a, team-b]
resync_interval: 5m
debounce_window: 2s
read_only: false
leader_election:
  enabled: true
  namespace: trickster
  name: my-lease
  lease_duration: 30s
  renew_deadline: 20s
  retry_period: 4s
published_service:
  namespace: trickster
  name: gateway
defaults:
  routing_mode: endpoint
  cache_name: default
  timeout: 30s
  health_mode: probe
`
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(src), &o))
	o.Initialize()
	require.NoError(t, o.Validate())
	require.Equal(t, "/etc/kube/config", o.Connection.Kubeconfig)
	require.False(t, o.Connection.InCluster)
	require.Equal(t, "example.com/gw", o.GatewayClassControllerName)
	require.Equal(t, "trickster", o.IngressClass)
	require.Equal(t, []string{"team-a", "team-b"}, o.WatchNamespaces)
	require.Equal(t, timeconv.Duration(5*time.Minute), o.ResyncInterval)
	require.True(t, o.WritesCluster())
	require.Equal(t, "my-lease", o.LeaderElection.Name)
	require.Equal(t, "trickster/gateway", o.PublishedService.String())
	require.Equal(t, RoutingModeEndpoint, o.RoutingMode())
	require.Equal(t, ao.HealthModeProbe, o.Defaults.HealthMode)

	b, err := yaml.Marshal(&o)
	require.NoError(t, err)
	var back Options
	require.NoError(t, yaml.Unmarshal(b, &back))
	require.Equal(t, o, back)

	// an empty section round trips to nothing but its defaults
	empty, err := yaml.Marshal(&Options{})
	require.NoError(t, err)
	require.Equal(t, "{}\n", string(empty))
}

// Generated backends inherit an access log from this block; an invalid one
// must be rejected here rather than at the first generated backend
func TestValidateDefaultsAccessLog(t *testing.T) {
	o := valid(t)
	o.Defaults.AccessLog = &alo.Options{Filename: "stdout", Format: "combined"}
	require.NoError(t, o.Validate())

	c := o.Clone()
	require.Equal(t, o.Defaults.AccessLog, c.Defaults.AccessLog)
	c.Defaults.AccessLog.Filename = "stderr"
	require.Equal(t, "stdout", o.Defaults.AccessLog.Filename,
		"the cloned access log must be independent")

	bad := valid(t)
	bad.Defaults.AccessLog = &alo.Options{Filename: "stdout", Format: "%nope"}
	require.ErrorContains(t, bad.Validate(), "defaults.access_log")
}

// Generated templates in the probe health mode carry this health check; an
// invalid one is the operator's mistake and fails validation here
func TestValidateDefaultsHealthCheck(t *testing.T) {
	o := valid(t)
	o.Defaults.HealthCheck = &ho.Options{Path: "/healthz", Verb: "GET"}
	require.NoError(t, o.Validate())

	c := o.Clone()
	require.Equal(t, o.Defaults.HealthCheck, c.Defaults.HealthCheck)
	c.Defaults.HealthCheck.Path = "/ready"
	require.Equal(t, "/healthz", o.Defaults.HealthCheck.Path,
		"the cloned health check must be independent")

	bad := valid(t)
	bad.Defaults.HealthCheck = &ho.Options{Verb: "FETCH"}
	require.ErrorContains(t, bad.Validate(), "defaults.healthcheck")
}

// An absent leader_election block is the default set, not a nil dereference
func TestValidateAbsentLeaderElection(t *testing.T) {
	o := valid(t)
	o.LeaderElection = nil
	require.NoError(t, o.Validate())
	require.True(t, o.ElectsLeader(), "an absent block still elects")
}

// The listeners claimed Ingresses are served on are named the way a backend
// names them, and naming none means the default frontend, which is what a
// backend that names no listener gets
func TestIngressListenerDefaults(t *testing.T) {
	require.Equal(t, []string{listener.DefaultFrontendName}, valid(t).Listeners())

	// an absent section, and an absent block within it, read the same way
	var absent *Options
	require.Equal(t, []string{listener.DefaultFrontendName}, absent.Listeners())
	supplied := &Options{}
	supplied.Initialize()
	require.Equal(t, []string{listener.DefaultFrontendName}, supplied.Listeners())
}

func TestIngressListenersConfigured(t *testing.T) {
	o := valid(t)
	o.Ingress = &IngressOptions{ListenerNames: []string{"web", "websecure"}}
	require.NoError(t, o.Validate())
	require.Equal(t, []string{"web", "websecure"}, o.Listeners())

	// the returned slice is the caller's, not the configuration's
	o.Listeners()[0] = "changed"
	require.Equal(t, "web", o.Ingress.ListenerNames[0])

	c := o.Clone()
	c.Ingress.ListenerNames[0] = "other"
	require.Equal(t, "web", o.Ingress.ListenerNames[0],
		"the cloned listener names must be independent")
}

func TestIngressListenerValidation(t *testing.T) {
	tests := []struct {
		name   string
		names  []string
		target error
	}{
		{"empty name", []string{"web", ""}, ErrEmptyListenerName},
		{"duplicate", []string{"web", "web"}, ErrDuplicateListenerName},
		{"reserved mgmt", []string{mgmt.ListenerNameMgmt}, ErrReservedListenerName},
		{"reserved metrics", []string{mgmt.ListenerNameMetrics}, ErrReservedListenerName},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := valid(t)
			o.Ingress = &IngressOptions{ListenerNames: test.names}
			require.ErrorIs(t, o.Validate(), test.target)
		})
	}
}
