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

// Package kubernetes defines the top-level 'kubernetes' section that turns on and shapes the
// Gateway/Ingress controller; without it there are no watches and nothing generated
package kubernetes

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	kubeopts "github.com/trickstercache/trickster/v2/pkg/kube/options"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// Routing modes for generated backends. There is no default: the two fail differently, and a
// guessed one would make a misconfiguration look like a routing bug.
const (
	// RoutingModeService sends traffic to the Service's cluster IP and lets
	// kube-proxy load balance
	RoutingModeService = "service"
	// RoutingModeEndpoint discovers the Service's endpoints and load
	// balances across them in-process
	RoutingModeEndpoint = "endpoint"
)

const (
	// DefaultGatewayClassControllerName is the controllerName Trickster
	// claims GatewayClasses with
	DefaultGatewayClassControllerName = appinfo.Domain + "/gateway-controller"
	// DefaultResyncInterval is the informer resync period; a backstop for
	// missed watch events, not the primary update path
	DefaultResyncInterval = 10 * time.Minute
	// DefaultDebounceWindow coalesces a burst of watch events into one
	// translation pass
	DefaultDebounceWindow = time.Second
	// DefaultProbeInterval is how often a generated ALB in the 'probe' health
	// mode probes each discovered member when defaults.healthcheck is unset
	DefaultProbeInterval = 5 * time.Second
	// DefaultLeaseName is the Lease object leader election contends on
	DefaultLeaseName = "trickster-gateway-controller"
	// DefaultLeaseDuration is how long a lease is held without renewal
	DefaultLeaseDuration = 15 * time.Second
	// DefaultRenewDeadline is how long the leader retries renewal before
	// yielding
	DefaultRenewDeadline = 10 * time.Second
	// DefaultRetryPeriod is the interval between leader election attempts
	DefaultRetryPeriod = 2 * time.Second
	// LeaderJitterFactor is the random spread applied to the retry period, so a renewal
	// must fit within the deadline even when the retry lands late
	LeaderJitterFactor = 1.2
)

var (
	// ErrRoutingModeRequired indicates defaults.routing_mode was not set
	ErrRoutingModeRequired = errors.New(
		"'defaults.routing_mode' is required and must be 'service' or 'endpoint'")
	// ErrInvalidRoutingMode indicates an unknown defaults.routing_mode
	ErrInvalidRoutingMode = errors.New(
		"'defaults.routing_mode' must be 'service' or 'endpoint'")
	// ErrInvalidHealthMode indicates an unknown defaults.health_mode
	ErrInvalidHealthMode = errors.New(
		"'defaults.health_mode' must be 'probe' or 'provider'")
	// ErrControllerNameRequired indicates the gateway class controller name
	// was explicitly blanked
	ErrControllerNameRequired = errors.New(
		"'gateway_class_controller_name' cannot be empty")
	// ErrNamespaceScopeConflict indicates both namespace selectors were set
	ErrNamespaceScopeConflict = errors.New(
		"'watch_namespaces' and 'namespace_selector' are mutually exclusive")
	// ErrNegativeInterval indicates a negative duration
	ErrNegativeInterval = errors.New("interval cannot be negative")
	// ErrIncompletePublishedService indicates a partially-specified reference
	ErrIncompletePublishedService = errors.New(
		"'published_service' requires both 'namespace' and 'name'")
	// ErrPublishedServiceReadOnly indicates a published service on an
	// instance that writes nothing to the cluster
	ErrPublishedServiceReadOnly = errors.New(
		"'published_service' has no effect when 'read_only' is set, " +
			"because its addresses are only ever written into status")
	// ErrLeaseDurationTooShort indicates a lease under one second, which the
	// Lease API stores in whole seconds and so would read as expired
	ErrLeaseDurationTooShort = errors.New(
		"'leader_election.lease_duration' must be at least 1s")
	// ErrRenewDeadlineTooLong indicates a renew deadline at or above the
	// lease duration, which cannot renew before the lease expires
	ErrRenewDeadlineTooLong = errors.New(
		"'leader_election.renew_deadline' must be less than 'lease_duration'")
	// ErrEmptyListenerName indicates a blank entry in ingress.listener_names
	ErrEmptyListenerName = errors.New(
		"'ingress.listener_names' cannot contain an empty name")
	// ErrDuplicateListenerName indicates a repeated ingress listener name
	ErrDuplicateListenerName = errors.New(
		"'ingress.listener_names' cannot name a listener twice")
	// ErrReservedListenerName indicates ingress.listener_names named a
	// listener reserved for management endpoints
	ErrReservedListenerName = errors.New(
		"'ingress.listener_names' cannot name a reserved listener")
	// ErrRetryPeriodTooLong indicates a retry period that, with jitter, cannot
	// retry before the renew deadline passes
	ErrRetryPeriodTooLong = errors.New(
		"'leader_election.retry_period' must be less than 'renew_deadline' divided by 1.2")
)

// Options configures the Kubernetes Gateway/Ingress controller
type Options struct {
	// Enabled turns the controller on; true by default when the section is present, so
	// 'kubernetes: {}' runs it, and false keeps the configuration while shutting it off
	Enabled *bool `yaml:"enabled,omitempty"`
	// Connection is the Kubernetes API connection, sharing the vocabulary
	// and the informer registry with the autodiscovery provider
	Connection *kubeopts.Options `yaml:"connection,omitempty"`
	// GatewayClassControllerName is the controllerName this instance claims
	// GatewayClasses with; classes naming any other controller are ignored
	GatewayClassControllerName string `yaml:"gateway_class_controller_name,omitempty"`
	// IngressClass is the IngressClass name this instance claims; empty claims only Ingresses
	// pointing at an IngressClass marked as the cluster default
	IngressClass string `yaml:"ingress_class,omitempty"`
	// WatchNamespaces restricts the controller to these namespaces; empty
	// watches all. Mutually exclusive with NamespaceSelector.
	WatchNamespaces []string `yaml:"watch_namespaces,omitempty"`
	// NamespaceSelector restricts the controller to namespaces carrying
	// these labels. Mutually exclusive with WatchNamespaces.
	NamespaceSelector map[string]string `yaml:"namespace_selector,omitempty"`
	// ResyncInterval is the informer resync period; 0 takes the default and
	// a negative value is invalid. It is a backstop for missed events.
	ResyncInterval timeconv.Duration `yaml:"resync_interval,omitempty"`
	// DebounceWindow coalesces a burst of watch events into one translation
	// pass, so a rollout touching many objects yields one reload
	DebounceWindow timeconv.Duration `yaml:"debounce_window,omitempty"`
	// ReadOnly stops every write to the Kubernetes API (status, Events, the election Lease);
	// the instance still watches, translates and serves
	ReadOnly bool `yaml:"read_only,omitempty"`
	// LeaderElection controls which replica writes status and Events
	LeaderElection *LeaderElectionOptions `yaml:"leader_election,omitempty"`
	// Ingress shapes how Ingress v1 objects are served; an Ingress cannot describe its port, so
	// unlike the Gateway API it names the listeners that serve it
	Ingress *IngressOptions `yaml:"ingress,omitempty"`
	// PublishedService names the Service whose addresses are published into
	// Gateway and Ingress status; without it, no address is published
	PublishedService *ServiceRef `yaml:"published_service,omitempty"`
	// Defaults shapes the backends the controller generates
	Defaults *DefaultsOptions `yaml:"defaults,omitempty"`
}

// LeaderElectionOptions controls the Lease election deciding which replica writes status and
// Events; every replica programs its own data plane, so losing never stops it serving
type LeaderElectionOptions struct {
	// Enabled defaults to true: two replicas writing status to one object
	// conflict in ways that are hard to diagnose after the fact
	Enabled *bool `yaml:"enabled,omitempty"`
	// Namespace holds the Lease; empty uses the pod's own namespace
	Namespace string `yaml:"namespace,omitempty"`
	// Name is the Lease object name
	Name string `yaml:"name,omitempty"`
	// LeaseDuration is how long a lease is held without renewal
	LeaseDuration timeconv.Duration `yaml:"lease_duration,omitempty"`
	// RenewDeadline is how long the leader retries renewal before yielding;
	// must be less than LeaseDuration
	RenewDeadline timeconv.Duration `yaml:"renew_deadline,omitempty"`
	// RetryPeriod is the interval between election attempts; must be less
	// than RenewDeadline
	RetryPeriod timeconv.Duration `yaml:"retry_period,omitempty"`
}

// IngressOptions shapes how Ingress v1 objects are served.
type IngressOptions struct {
	// ListenerNames are the listeners claimed Ingresses are served on, named as a backend names
	// its listeners; ports, limits and TLS are the configuration of the listeners themselves
	ListenerNames []string `yaml:"listener_names,omitempty"`
}

// Listeners returns the listeners claimed Ingresses are served on; naming none serves them on
// the default frontend, as a backend that names no listener is
func (o *Options) Listeners() []string {
	if o == nil || o.Ingress == nil || len(o.Ingress.ListenerNames) == 0 {
		return []string{listener.DefaultFrontendName}
	}
	return slices.Clone(o.Ingress.ListenerNames)
}

func (i *IngressOptions) validate() error {
	if i == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(i.ListenerNames))
	for _, name := range i.ListenerNames {
		if name == "" {
			return ErrEmptyListenerName
		}
		if name == mgmt.ListenerNameMgmt || name == mgmt.ListenerNameMetrics {
			return fmt.Errorf("%w (got %q)", ErrReservedListenerName, name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("%w (got %q)", ErrDuplicateListenerName, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// ServiceRef names a namespaced Kubernetes Service
type ServiceRef struct {
	Namespace string `yaml:"namespace,omitempty"`
	Name      string `yaml:"name,omitempty"`
}

// String renders the reference as namespace/name
func (s *ServiceRef) String() string {
	if s == nil {
		return ""
	}
	return s.Namespace + "/" + s.Name
}

// DefaultsOptions shapes the backends the controller generates. A route that
// asks for something different overrides these per route.
type DefaultsOptions struct {
	// RoutingMode is 'service' or 'endpoint'; it is required, with no default
	RoutingMode string `yaml:"routing_mode,omitempty"`
	// CacheName is the configured cache generated caching backends use
	CacheName string `yaml:"cache_name,omitempty"`
	// NegativeCacheName is the configured negative cache generated caching backends use; a route
	// may name another, unlike the names below, which select infrastructure or a capability
	NegativeCacheName string `yaml:"negative_cache_name,omitempty"`
	// TracingName is the configured tracer generated backends report to
	TracingName string `yaml:"tracing_name,omitempty"`
	// ReqRewriterName is a configured request rewriter every generated
	// backend runs. A route's own rewrites run after it.
	ReqRewriterName string `yaml:"req_rewriter_name,omitempty"`
	// AuthenticatorName is the configured authenticator every generated backend is behind; no
	// annotation sets it, since one that could name it could also omit it
	AuthenticatorName string `yaml:"authenticator_name,omitempty"`
	// Timeout is the upstream timeout for generated backends
	Timeout timeconv.Duration `yaml:"timeout,omitempty"`
	// HealthMode is the health mode of generated discovery-backed ALBs; 'provider' by default,
	// because EndpointSlice readiness already answers what active probes would ask
	HealthMode string `yaml:"health_mode,omitempty"`
	// HealthCheck is the active probe discovered members run in the 'probe' health mode; unset
	// probes the origin root every DefaultProbeInterval, and 'provider' mode never reads it
	HealthCheck *ho.Options `yaml:"healthcheck,omitempty"`
	// AccessLog is the access log configuration generated backends inherit;
	// unset means they inherit the top-level access_log
	AccessLog *alo.Options `yaml:"access_log,omitempty"`
}

// New returns an Options with default values, as though an empty 'kubernetes:' section had
// been supplied; RoutingMode is deliberately left unset
func New() *Options {
	return &Options{
		Enabled:                    new(true),
		Connection:                 kubeopts.New(),
		GatewayClassControllerName: DefaultGatewayClassControllerName,
		ResyncInterval:             timeconv.Duration(DefaultResyncInterval),
		DebounceWindow:             timeconv.Duration(DefaultDebounceWindow),
		LeaderElection: &LeaderElectionOptions{
			Enabled:       new(true),
			Name:          DefaultLeaseName,
			LeaseDuration: timeconv.Duration(DefaultLeaseDuration),
			RenewDeadline: timeconv.Duration(DefaultRenewDeadline),
			RetryPeriod:   timeconv.Duration(DefaultRetryPeriod),
		},
		Defaults: &DefaultsOptions{HealthMode: ao.HealthModeProvider},
	}
}

// IsEnabled reports whether the controller should run
func (o *Options) IsEnabled() bool {
	return o != nil && (o.Enabled == nil || *o.Enabled)
}

// WritesCluster reports whether this instance may write to the Kubernetes API: status, Events
// and the election Lease, which are the controller's only writes and its only write verbs
func (o *Options) WritesCluster() bool {
	return o.IsEnabled() && !o.ReadOnly
}

// ElectsLeader reports whether leader election is on; a read-only instance elects nobody, since
// the election only decides who writes
func (o *Options) ElectsLeader() bool {
	if !o.WritesCluster() {
		return false
	}
	if o.LeaderElection == nil {
		return true
	}
	return o.LeaderElection.Enabled == nil || *o.LeaderElection.Enabled
}

// RoutingMode returns the configured routing mode for generated backends
func (o *Options) RoutingMode() string {
	if o == nil || o.Defaults == nil {
		return ""
	}
	return o.Defaults.RoutingMode
}

// Initialize applies defaults to a section supplied by configuration
func (o *Options) Initialize() {
	if o == nil {
		return
	}
	if o.Enabled == nil {
		o.Enabled = new(true)
	}
	if o.Connection == nil {
		o.Connection = kubeopts.New()
	}
	o.Connection.Initialize()
	if o.GatewayClassControllerName == "" {
		o.GatewayClassControllerName = DefaultGatewayClassControllerName
	}
	if o.ResyncInterval == 0 {
		o.ResyncInterval = timeconv.Duration(DefaultResyncInterval)
	}
	if o.DebounceWindow == 0 {
		o.DebounceWindow = timeconv.Duration(DefaultDebounceWindow)
	}
	if o.LeaderElection == nil {
		o.LeaderElection = &LeaderElectionOptions{}
	}
	o.LeaderElection.initialize()
	if o.Defaults == nil {
		o.Defaults = &DefaultsOptions{}
	}
	if o.Defaults.HealthMode == "" {
		o.Defaults.HealthMode = ao.HealthModeProvider
	}
}

func (l *LeaderElectionOptions) initialize() {
	if l.Enabled == nil {
		l.Enabled = new(true)
	}
	if l.Name == "" {
		l.Name = DefaultLeaseName
	}
	if l.LeaseDuration == 0 {
		l.LeaseDuration = timeconv.Duration(DefaultLeaseDuration)
	}
	if l.RenewDeadline == 0 {
		l.RenewDeadline = timeconv.Duration(DefaultRenewDeadline)
	}
	if l.RetryPeriod == 0 {
		l.RetryPeriod = timeconv.Duration(DefaultRetryPeriod)
	}
}

// Validate validates the Options; a disabled section is not validated, so turning the
// controller off does not require keeping the rest of the block valid
func (o *Options) Validate() error {
	if o == nil || !o.IsEnabled() {
		return nil
	}
	if err := o.Connection.Validate(); err != nil {
		return fmt.Errorf("kubernetes 'connection': %w", err)
	}
	if o.GatewayClassControllerName == "" {
		return ErrControllerNameRequired
	}
	if len(o.WatchNamespaces) > 0 && len(o.NamespaceSelector) > 0 {
		return ErrNamespaceScopeConflict
	}
	if o.ResyncInterval < 0 || o.DebounceWindow < 0 {
		return ErrNegativeInterval
	}
	if o.PublishedService != nil {
		if o.PublishedService.Namespace == "" || o.PublishedService.Name == "" {
			return ErrIncompletePublishedService
		}
		// the addresses it supplies exist only to be written into status
		if o.ReadOnly {
			return ErrPublishedServiceReadOnly
		}
	}
	if err := o.LeaderElection.validate(); err != nil {
		return err
	}
	if err := o.Ingress.validate(); err != nil {
		return err
	}
	return o.Defaults.validate()
}

func (l *LeaderElectionOptions) validate() error {
	if l == nil {
		return nil
	}
	if l.LeaseDuration < 0 || l.RenewDeadline < 0 || l.RetryPeriod < 0 {
		return ErrNegativeInterval
	}
	if l.LeaseDuration > 0 && l.LeaseDuration < timeconv.Duration(time.Second) {
		return ErrLeaseDurationTooShort
	}
	// a renewal that cannot complete before the lease expires produces
	// leadership that flaps rather than one that holds
	if l.LeaseDuration > 0 && l.RenewDeadline >= l.LeaseDuration {
		return ErrRenewDeadlineTooLong
	}
	if l.RenewDeadline > 0 &&
		float64(l.RetryPeriod)*LeaderJitterFactor >= float64(l.RenewDeadline) {
		return ErrRetryPeriodTooLong
	}
	return nil
}

func (d *DefaultsOptions) validate() error {
	if d == nil || d.RoutingMode == "" {
		return ErrRoutingModeRequired
	}
	switch d.RoutingMode {
	case RoutingModeService, RoutingModeEndpoint:
	default:
		return fmt.Errorf("%w (got %q)", ErrInvalidRoutingMode, d.RoutingMode)
	}
	switch d.HealthMode {
	case "", ao.HealthModeProbe, ao.HealthModeProvider:
	default:
		return fmt.Errorf("%w (got %q)", ErrInvalidHealthMode, d.HealthMode)
	}
	if d.Timeout < 0 {
		return ErrNegativeInterval
	}
	if d.AccessLog != nil {
		if _, err := d.AccessLog.Validate(); err != nil {
			return fmt.Errorf("kubernetes 'defaults.access_log': %w", err)
		}
	}
	if d.HealthCheck != nil {
		if _, err := d.HealthCheck.Validate(); err != nil {
			return fmt.Errorf("kubernetes 'defaults.healthcheck': %w", err)
		}
	}
	return nil
}

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := pointers.Clone(o)
	out.Enabled = pointers.Clone(o.Enabled)
	out.Connection = o.Connection.Clone()
	out.WatchNamespaces = append([]string(nil), o.WatchNamespaces...)
	if o.NamespaceSelector != nil {
		out.NamespaceSelector = maps.Clone(o.NamespaceSelector)
	}
	if o.LeaderElection != nil {
		out.LeaderElection = pointers.Clone(o.LeaderElection)
		out.LeaderElection.Enabled = pointers.Clone(o.LeaderElection.Enabled)
	}
	if o.PublishedService != nil {
		out.PublishedService = pointers.Clone(o.PublishedService)
	}
	if o.Ingress != nil {
		out.Ingress = &IngressOptions{
			ListenerNames: slices.Clone(o.Ingress.ListenerNames),
		}
	}
	if o.Defaults != nil {
		out.Defaults = pointers.Clone(o.Defaults)
		if o.Defaults.HealthCheck != nil {
			out.Defaults.HealthCheck = o.Defaults.HealthCheck.Clone()
		}
		if o.Defaults.AccessLog != nil {
			out.Defaults.AccessLog = o.Defaults.AccessLog.Clone()
		}
	}
	return out
}
