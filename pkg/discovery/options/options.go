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

// Package options defines the configuration options for autodiscovery
// discoverers. A discoverer is a named, connection-level configuration
// (provider + client settings) declared in the top-level 'discovery' config
// section; ALBs bind to a discoverer by name via 'alb.discovery' and supply
// a provider-specific Query.
package options

import (
	"maps"
	"net"
	"net/url"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"

	"go.yaml.in/yaml/v3"
)

// Lookup is a map of discoverer Options keyed by discoverer name
type Lookup map[string]*Options

// Options defines a named discoverer: the provider and its connection-level
// settings. Query-level settings (what to select) live with the consumer in
// alb.discovery, so that multiple ALBs can share one discoverer's
// client/informer/resolver set.
type Options struct {
	// Provider is the name of the autodiscovery provider
	// (kubernetes, dns_srv, dns_a, file)
	Provider string `yaml:"provider,omitempty"`
	// Kubernetes provides client connection settings when Provider is 'kubernetes'
	Kubernetes *KubernetesOptions `yaml:"kubernetes,omitempty"`
	// DNS provides resolver settings when Provider is 'dns_srv' or 'dns_a'
	DNS *DNSOptions `yaml:"dns,omitempty"`
	// File provides change-detection settings when Provider is 'file'
	File *FileOptions `yaml:"file,omitempty"`
	// HTTPSD provides payload settings when Provider is 'http_sd'
	HTTPSD *HTTPSDOptions `yaml:"http_sd,omitempty"`
	//
	// HTTP provides the outbound client settings shared by every provider
	// that discovers members by polling an HTTP endpoint
	HTTP *HTTPOptions `yaml:"http,omitempty"`
	//
	// synthetic values
	// Name is the name of the discoverer, taken from the key in the Lookup map
	Name string `yaml:"-"`
}

// KubernetesOptions defines the Kubernetes API client settings for a
// discoverer with the 'kubernetes' provider
type KubernetesOptions struct {
	// InCluster, when true, uses the pod's service account for API access.
	// Defaults to true when no kubeconfig is provided.
	InCluster bool `yaml:"in_cluster,omitempty"`
	// Kubeconfig is the path to a kubeconfig file, for use when running
	// outside the target cluster. Mutually exclusive with in_cluster.
	Kubeconfig string `yaml:"kubeconfig,omitempty"`
}

// DNSOptions defines the resolver settings for a discoverer with the
// 'dns_srv' or 'dns_a' provider
type DNSOptions struct {
	// Resolver is the host:port of the DNS server to query. When empty, the
	// system resolver is used.
	Resolver string `yaml:"resolver,omitempty"`
	// Interval is the poll cadence for re-resolving records. Record TTLs act
	// as a floor: a record is never re-resolved before its TTL expires.
	Interval timeconv.Duration `yaml:"interval,omitempty"`
}

// FileOptions defines the change-detection settings for a discoverer with
// the 'file' provider. The provider always watches the member-list file's
// parent directory for filesystem change notifications AND stat-polls the
// file as a fallback; poll_interval controls that fallback cadence. On
// filesystems where change notification is unreliable or unavailable --
// NFS-backed volumes, some FUSE/CSI mounts -- the poll is the effective
// update mechanism, so lower it to the freshness the deployment needs.
type FileOptions struct {
	// PollInterval is the cadence of the stat-based change poll that
	// backstops filesystem change notification
	PollInterval timeconv.Duration `yaml:"poll_interval,omitempty"`
}

// HTTPOptions defines the outbound HTTP client settings for a discoverer
// whose provider polls an HTTP endpoint. It is deliberately shared rather
// than per-provider: every HTTP-based provider needs the same endpoint,
// cadence, TLS and credential vocabulary, and an operator configuring two
// of them should not have to learn it twice.
//
// Deadlines have one owner. Timeout bounds a single poll by way of the
// poller's iteration context; there is no separate client timeout that
// could truncate a provider's long-poll from underneath it.
type HTTPOptions struct {
	// Endpoint is the base URL of the service to poll (scheme://host[:port])
	Endpoint string `yaml:"endpoint,omitempty"`
	// Interval is the poll cadence
	Interval timeconv.Duration `yaml:"interval,omitempty"`
	// Timeout bounds a single poll. Providers using blocking queries, whose
	// server-side wait is part of a normal poll, need this comfortably
	// above that wait.
	Timeout timeconv.Duration `yaml:"timeout,omitempty"`
	// TLS configures outbound client TLS: mutual-auth client certificate,
	// additional Certificate Authorities, and the verification escape hatch
	TLS *to.Options `yaml:"tls,omitempty"`
	// Headers are set on every request. Registries whose credential is a
	// bespoke header (Consul's X-Consul-Token, Nomad's X-Nomad-Token) use
	// this rather than a per-provider field.
	Headers types.EnvStringMap `yaml:"headers,omitempty"`
	// Username and Password form a static HTTP Basic credential; mutually
	// exclusive with the bearer-token fields
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	// BearerToken is sent as 'Authorization: Bearer <token>'; mutually
	// exclusive with username/password and with BearerTokenFile
	BearerToken string `yaml:"bearer_token,omitempty"`
	// BearerTokenFile is a path read for the bearer token before each poll,
	// so that a rotated credential (a Vault-issued Consul token, a
	// projected kubernetes service-account token) is picked up without a
	// restart. Preferred over BearerToken for anything that expires.
	BearerTokenFile string `yaml:"bearer_token_file,omitempty"`
	// FollowRedirects allows the client to follow redirects. It defaults
	// false: a discoverer wants the answer from the endpoint it was
	// pointed at, and a redirect elsewhere is a fact to surface rather than
	// to chase.
	FollowRedirects bool `yaml:"follow_redirects,omitempty"`
}

// Member-list document formats accepted by the http_sd provider
const (
	// FormatTrickster is Trickster's native member list, carrying scheme,
	// path prefix, weight and replica group per member
	FormatTrickster = "trickster"
	// FormatPrometheus is the document Prometheus's own file_sd and http_sd
	// consume: [{"targets": [...], "labels": {...}}]
	FormatPrometheus = "prometheus"
)

// HTTPSDOptions defines the payload settings for a discoverer with the
// 'http_sd' provider. Connection settings live in the shared 'http' block.
type HTTPSDOptions struct {
	// Format names the member-list document the endpoint serves:
	// 'trickster' (default) or 'prometheus'.
	//
	// It is explicit rather than sniffed. The two documents are structurally
	// distinguishable, but guessing means a typo in one format can parse as
	// a valid, wrong membership in the other -- and the cost of guessing
	// wrong is a silently drained pool.
	Format string `yaml:"format,omitempty"`
}

const (
	// DefaultHTTPInterval is the default poll cadence for HTTP-based
	// discovery providers
	DefaultHTTPInterval = 30 * time.Second
	// MinimumHTTPInterval is the lowest permitted HTTP poll cadence
	MinimumHTTPInterval = time.Second
	// DefaultHTTPTimeout is the default single-poll timeout for HTTP-based
	// discovery providers
	DefaultHTTPTimeout = 10 * time.Second
	// MinimumHTTPTimeout is the lowest permitted single-poll timeout
	MinimumHTTPTimeout = time.Millisecond * 100
)

const (
	// DefaultDNSInterval is the default DNS poll cadence
	DefaultDNSInterval = 30 * time.Second
	// MinimumDNSInterval is the lowest permitted DNS poll cadence
	MinimumDNSInterval = time.Second
	// DefaultFilePollInterval is the default member-file stat-poll cadence
	DefaultFilePollInterval = 30 * time.Second
	// MinimumFilePollInterval is the lowest permitted member-file
	// stat-poll cadence
	MinimumFilePollInterval = time.Second
)

var _ types.ConfigOptions[Options] = &Options{}

// New returns a new discoverer Options with default values
func New() *Options {
	return &Options{}
}

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options {
	out := pointers.Clone(o)
	if o.Kubernetes != nil {
		out.Kubernetes = pointers.Clone(o.Kubernetes)
	}
	if o.DNS != nil {
		out.DNS = pointers.Clone(o.DNS)
	}
	if o.File != nil {
		out.File = pointers.Clone(o.File)
	}
	if o.HTTPSD != nil {
		out.HTTPSD = pointers.Clone(o.HTTPSD)
	}
	if o.HTTP != nil {
		out.HTTP = pointers.Clone(o.HTTP)
		if o.HTTP.TLS != nil {
			out.HTTP.TLS = o.HTTP.TLS.Clone()
		}
		if o.HTTP.Headers != nil {
			out.HTTP.Headers = maps.Clone(o.HTTP.Headers)
		}
	}
	return out
}

// Initialize sets defaults on the Options based on the configured provider
func (o *Options) Initialize(name string) error {
	if name != "" {
		o.Name = name
	}
	switch o.Provider {
	case providers.Kubernetes:
		if o.Kubernetes == nil {
			o.Kubernetes = &KubernetesOptions{InCluster: true}
		} else if !o.Kubernetes.InCluster && o.Kubernetes.Kubeconfig == "" {
			o.Kubernetes.InCluster = true
		}
	case providers.DNSSRV, providers.DNSA:
		if o.DNS == nil {
			o.DNS = &DNSOptions{}
		}
		if o.DNS.Interval == 0 {
			o.DNS.Interval = timeconv.Duration(DefaultDNSInterval)
		}
	case providers.File:
		if o.File == nil {
			o.File = &FileOptions{}
		}
		if o.File.PollInterval == 0 {
			o.File.PollInterval = timeconv.Duration(DefaultFilePollInterval)
		}
	}
	if o.Provider == providers.HTTPSD {
		if o.HTTPSD == nil {
			o.HTTPSD = &HTTPSDOptions{}
		}
		if o.HTTPSD.Format == "" {
			o.HTTPSD.Format = FormatTrickster
		}
	}
	if o.HTTP != nil {
		if o.HTTP.Interval == 0 {
			o.HTTP.Interval = timeconv.Duration(DefaultHTTPInterval)
		}
		if o.HTTP.Timeout == 0 {
			o.HTTP.Timeout = timeconv.Duration(DefaultHTTPTimeout)
		}
	}
	return nil
}

// Validate validates the Options
func (o *Options) Validate() (bool, error) {
	if o.Provider == "" {
		return false, NewErrMissingDiscoveryProvider(o.Name)
	}
	if !providers.IsValidProvider(o.Provider) {
		return false, NewErrInvalidDiscoveryProvider(o.Provider, o.Name)
	}
	if o.Kubernetes != nil && o.Provider != providers.Kubernetes {
		return false, NewErrInvalidDiscoveryBlock("kubernetes", o.Provider, o.Name)
	}
	if o.DNS != nil && o.Provider != providers.DNSSRV && o.Provider != providers.DNSA {
		return false, NewErrInvalidDiscoveryBlock("dns", o.Provider, o.Name)
	}
	if o.File != nil && o.Provider != providers.File {
		return false, NewErrInvalidDiscoveryBlock("file", o.Provider, o.Name)
	}
	if o.HTTP != nil && !providers.IsHTTPProvider(o.Provider) {
		return false, NewErrInvalidDiscoveryBlock("http", o.Provider, o.Name)
	}
	if o.HTTPSD != nil && o.Provider != providers.HTTPSD {
		return false, NewErrInvalidDiscoveryBlock("http_sd", o.Provider, o.Name)
	}
	if o.Provider == providers.HTTPSD {
		if o.HTTP == nil {
			return false, NewErrInvalidHTTPOptions(o.Name,
				"the 'http' block is required for the http_sd provider")
		}
		if f := o.HTTPSD.GetFormat(); f != FormatTrickster && f != FormatPrometheus {
			return false, NewErrInvalidHTTPSDOptions(o.Name,
				"'format' must be trickster or prometheus")
		}
	}
	if err := o.validateHTTP(); err != nil {
		return false, err
	}
	if o.File != nil && o.File.PollInterval != 0 &&
		time.Duration(o.File.PollInterval) < MinimumFilePollInterval {
		return false, NewErrInvalidFileOptions(o.Name,
			"'poll_interval' must be at least 1s")
	}
	if o.Kubernetes != nil && o.Kubernetes.InCluster && o.Kubernetes.Kubeconfig != "" {
		return false, NewErrInvalidKubernetesOptions(o.Name,
			"'in_cluster' and 'kubeconfig' are mutually exclusive")
	}
	if o.DNS != nil {
		if o.DNS.Resolver != "" {
			if _, _, err := net.SplitHostPort(o.DNS.Resolver); err != nil {
				return false, NewErrInvalidDNSOptions(o.Name,
					"'resolver' must be a host:port")
			}
		}
		if o.DNS.Interval != 0 &&
			time.Duration(o.DNS.Interval) < MinimumDNSInterval {
			return false, NewErrInvalidDNSOptions(o.Name,
				"'interval' must be at least 1s")
		}
	}
	return true, nil
}

// GetFormat returns the configured member-list format, defaulting to
// trickster. It tolerates a nil receiver so callers need not distinguish an
// absent block from an unset field.
func (o *HTTPSDOptions) GetFormat() string {
	if o == nil || o.Format == "" {
		return FormatTrickster
	}
	return o.Format
}

// validateHTTP validates the shared HTTP client options.
func (o *Options) validateHTTP() error {
	h := o.HTTP
	if h == nil {
		return nil
	}
	if h.Endpoint == "" {
		return NewErrInvalidHTTPOptions(o.Name, "'endpoint' is required")
	}
	u, err := url.Parse(h.Endpoint)
	if err != nil {
		return NewErrInvalidHTTPOptions(o.Name, "'endpoint' is not a valid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return NewErrInvalidHTTPOptions(o.Name,
			"'endpoint' must be an http or https url")
	}
	if u.Host == "" {
		return NewErrInvalidHTTPOptions(o.Name, "'endpoint' must include a host")
	}
	if h.Interval != 0 && time.Duration(h.Interval) < MinimumHTTPInterval {
		return NewErrInvalidHTTPOptions(o.Name, "'interval' must be at least 1s")
	}
	if h.Timeout != 0 && time.Duration(h.Timeout) < MinimumHTTPTimeout {
		return NewErrInvalidHTTPOptions(o.Name, "'timeout' must be at least 100ms")
	}
	// credentials are mutually exclusive: silently preferring one over the
	// other is how an operator ends up debugging a 401 against a config
	// that looks correct
	hasBasic := h.Username != "" || h.Password != ""
	hasBearer := h.BearerToken != "" || h.BearerTokenFile != ""
	if hasBasic && hasBearer {
		return NewErrInvalidHTTPOptions(o.Name,
			"'username'/'password' is mutually exclusive with 'bearer_token'/'bearer_token_file'")
	}
	if h.BearerToken != "" && h.BearerTokenFile != "" {
		return NewErrInvalidHTTPOptions(o.Name,
			"'bearer_token' and 'bearer_token_file' are mutually exclusive")
	}
	if h.Password != "" && h.Username == "" {
		return NewErrInvalidHTTPOptions(o.Name, "'password' requires 'username'")
	}
	return nil
}

// Initialize initializes each discoverer Options in the Lookup, assigning
// names from the map keys
func (l Lookup) Initialize() error {
	for k, o := range l {
		if o == nil {
			continue
		}
		if err := o.Initialize(k); err != nil {
			return err
		}
	}
	return nil
}

// Validate validates each discoverer Options in the Lookup
func (l Lookup) Validate() error {
	for k, o := range l {
		if o == nil {
			return NewErrInvalidDiscovererName(k)
		}
		o.Name = k
		if _, err := o.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns a perfect copy of the Lookup
func (l Lookup) Clone() Lookup {
	out := make(Lookup, len(l))
	for k, v := range l {
		if v != nil {
			out[k] = v.Clone()
		}
	}
	return out
}

func (o *Options) UnmarshalYAML(value *yaml.Node) error {
	type loadOptions Options
	lo := loadOptions(*New())
	if err := value.Decode(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
