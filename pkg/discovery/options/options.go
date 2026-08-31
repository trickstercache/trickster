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
	"net/url"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config/types"
	awsopts "github.com/trickstercache/trickster/v2/pkg/discovery/aws/options"
	consulopts "github.com/trickstercache/trickster/v2/pkg/discovery/consul/options"
	dnsopts "github.com/trickstercache/trickster/v2/pkg/discovery/dns/options"
	fileopts "github.com/trickstercache/trickster/v2/pkg/discovery/file/options"
	httpsdopts "github.com/trickstercache/trickster/v2/pkg/discovery/httpsd/options"
	kubeopts "github.com/trickstercache/trickster/v2/pkg/discovery/kubernetes/options"
	nomadopts "github.com/trickstercache/trickster/v2/pkg/discovery/nomad/options"
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
	Kubernetes *kubeopts.Options `yaml:"kubernetes,omitempty"`
	// DNS provides resolver settings when Provider is 'dns_srv' or 'dns_a'
	DNS *dnsopts.Options `yaml:"dns,omitempty"`
	// File provides change-detection settings when Provider is 'file'
	File *fileopts.Options `yaml:"file,omitempty"`
	// HTTPSD provides payload settings when Provider is 'http_sd'
	HTTPSD *httpsdopts.Options `yaml:"http_sd,omitempty"`
	// Consul provides catalog settings when Provider is 'consul'
	Consul *consulopts.Options `yaml:"consul,omitempty"`
	// Nomad provides registry settings when Provider is 'nomad'
	Nomad *nomadopts.Options `yaml:"nomad,omitempty"`
	// AWS provides API and credential settings when Provider is 'aws'
	AWS *awsopts.Options `yaml:"aws,omitempty"`
	//
	// HTTP provides the outbound client settings shared by every provider
	// that discovers members by polling an HTTP endpoint
	HTTP *HTTPOptions `yaml:"http,omitempty"`
	//
	// synthetic values
	// Name is the name of the discoverer, taken from the key in the Lookup map
	Name string `yaml:"-"`
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

var _ types.ConfigOptions[Options] = &Options{}

// New returns a new discoverer Options with default values
func New() *Options {
	return &Options{}
}

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options {
	out := pointers.Clone(o)
	out.Kubernetes = o.Kubernetes.Clone()
	out.DNS = o.DNS.Clone()
	out.File = o.File.Clone()
	out.HTTPSD = o.HTTPSD.Clone()
	out.Consul = o.Consul.Clone()
	out.Nomad = o.Nomad.Clone()
	out.AWS = o.AWS.Clone()
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

// Initialize sets defaults on the Options based on the configured provider,
// delegating each provider block's defaults to the package that owns it.
func (o *Options) Initialize(name string) error {
	if name != "" {
		o.Name = name
	}
	switch o.Provider {
	case providers.Kubernetes:
		if o.Kubernetes == nil {
			o.Kubernetes = kubeopts.New()
		}
		o.Kubernetes.Initialize()
	case providers.DNSSRV, providers.DNSA:
		if o.DNS == nil {
			o.DNS = dnsopts.New()
		}
		o.DNS.Initialize()
	case providers.File:
		if o.File == nil {
			o.File = fileopts.New()
		}
		o.File.Initialize()
	case providers.HTTPSD:
		if o.HTTPSD == nil {
			o.HTTPSD = httpsdopts.New()
		}
		o.HTTPSD.Initialize()
	case providers.Consul:
		if o.Consul == nil {
			o.Consul = consulopts.New()
		}
	case providers.Nomad:
		if o.Nomad == nil {
			o.Nomad = nomadopts.New()
		}
	case providers.AWS:
		if o.AWS == nil {
			o.AWS = awsopts.New()
		}
		// AWS derives its endpoint, so an http block is optional; create
		// one so the shared interval and timeout defaults apply
		if o.HTTP == nil {
			o.HTTP = &HTTPOptions{}
		}
	}
	if o.HTTP != nil {
		if o.HTTP.Interval == 0 {
			o.HTTP.Interval = timeconv.Duration(DefaultHTTPInterval)
		}
		if o.HTTP.Timeout == 0 {
			o.HTTP.Timeout = timeconv.Duration(o.defaultHTTPTimeout())
		}
	}
	return nil
}

// defaultHTTPTimeout returns the poll timeout appropriate to the provider.
// The blocking-query providers need one that outlasts their server-side
// wait; a shared 10s default would abort every long poll.
func (o *Options) defaultHTTPTimeout() time.Duration {
	switch o.Provider {
	case providers.Consul:
		return consulopts.PollTimeout(o.Consul.GetWait())
	case providers.Nomad:
		return nomadopts.PollTimeout(o.Nomad.GetWait())
	default:
		return DefaultHTTPTimeout
	}
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
	if o.Consul != nil && o.Provider != providers.Consul {
		return false, NewErrInvalidDiscoveryBlock("consul", o.Provider, o.Name)
	}
	if o.Nomad != nil && o.Provider != providers.Nomad {
		return false, NewErrInvalidDiscoveryBlock("nomad", o.Provider, o.Name)
	}
	if o.AWS != nil && o.Provider != providers.AWS {
		return false, NewErrInvalidDiscoveryBlock("aws", o.Provider, o.Name)
	}
	if err := o.validateProviderBlock(); err != nil {
		return false, err
	}
	if err := o.validateHTTP(); err != nil {
		return false, err
	}
	return true, nil
}

// validateProviderBlock delegates each provider block's validation to the
// package that owns it, wrapping the result with the discoverer's name.
//
// Blocks own their own rules; this function owns only the cross-block ones,
// which are the relationships a single block cannot see: whether a shared
// http block is required, and whether its timeout outlasts a blocking wait.
func (o *Options) validateProviderBlock() error {
	switch o.Provider {
	case providers.Kubernetes:
		if err := o.Kubernetes.Validate(); err != nil {
			return NewErrInvalidKubernetesOptions(o.Name, err.Error())
		}
	case providers.DNSSRV, providers.DNSA:
		if err := o.DNS.Validate(); err != nil {
			return NewErrInvalidDNSOptions(o.Name, err.Error())
		}
	case providers.File:
		if err := o.File.Validate(); err != nil {
			return NewErrInvalidFileOptions(o.Name, err.Error())
		}
	case providers.HTTPSD:
		if o.HTTP == nil {
			return NewErrInvalidHTTPOptions(o.Name,
				"the 'http' block is required for the http_sd provider")
		}
		if err := o.HTTPSD.Validate(); err != nil {
			return NewErrInvalidHTTPSDOptions(o.Name, err.Error())
		}
	case providers.Consul:
		if o.HTTP == nil {
			return NewErrInvalidHTTPOptions(o.Name,
				"the 'http' block is required for the consul provider")
		}
		if err := o.Consul.Validate(time.Duration(o.HTTP.Timeout)); err != nil {
			return NewErrInvalidConsulOptions(o.Name, err.Error())
		}
	case providers.Nomad:
		if o.HTTP == nil {
			return NewErrInvalidHTTPOptions(o.Name,
				"the 'http' block is required for the nomad provider")
		}
		if err := o.Nomad.Validate(time.Duration(o.HTTP.Timeout)); err != nil {
			return NewErrInvalidNomadOptions(o.Name, err.Error())
		}
	case providers.AWS:
		if err := o.AWS.Validate(); err != nil {
			return NewErrInvalidAWSOptions(o.Name, err.Error())
		}
	}
	return nil
}

// validateHTTP validates the shared HTTP client options.
func (o *Options) validateHTTP() error {
	h := o.HTTP
	if h == nil {
		return nil
	}
	if h.Endpoint == "" {
		// providers that compute their own endpoint treat this as an
		// optional override rather than a required setting
		if providers.DerivesEndpoint(o.Provider) {
			return o.validateHTTPTimings()
		}
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
	if err := o.validateHTTPTimings(); err != nil {
		return err
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

// validateHTTPTimings checks the cadence bounds shared by every HTTP-based
// provider, whether or not it supplies its own endpoint.
func (o *Options) validateHTTPTimings() error {
	h := o.HTTP
	if h.Interval != 0 && time.Duration(h.Interval) < MinimumHTTPInterval {
		return NewErrInvalidHTTPOptions(o.Name, "'interval' must be at least 1s")
	}
	if h.Timeout != 0 && time.Duration(h.Timeout) < MinimumHTTPTimeout {
		return NewErrInvalidHTTPOptions(o.Name, "'timeout' must be at least 100ms")
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
