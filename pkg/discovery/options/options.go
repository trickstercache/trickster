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
	"net"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
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
