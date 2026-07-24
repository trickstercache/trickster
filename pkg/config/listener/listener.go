/*
 * Copyright 2026 The Trickster Authors
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

// Package listener provides configuration for Trickster's inbound listeners.
package listener

import (
	"fmt"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	frontend "github.com/trickstercache/trickster/v2/pkg/frontend/options"
	metrics "github.com/trickstercache/trickster/v2/pkg/observability/metrics/options"
	"gopkg.in/yaml.v2"
)

const (
	// DefaultFrontendName is the listener used by backends that do not specify one.
	DefaultFrontendName = "default"
	// ProtocolHTTP is the HTTP listener protocol.
	ProtocolHTTP = "http"
)

var supportedProtocols = map[string]struct{}{
	ProtocolHTTP: {},
}

// Options describes one inbound listener.
type Options struct {
	// ListenAddress is the IP address for this listener's plaintext endpoint.
	ListenAddress string `yaml:"address,omitempty"`
	// ListenPort is the TCP port for this listener's plaintext endpoint.
	ListenPort int `yaml:"port,omitempty"`
	// TLSListenAddress is the IP address for this listener's TLS endpoint.
	TLSListenAddress string `yaml:"tls_address,omitempty"`
	// TLSListenPort is the TCP port for this listener's TLS endpoint.
	TLSListenPort int `yaml:"tls_port,omitempty"`
	// ConnectionsLimit is the maximum number of concurrent connections.
	ConnectionsLimit int `yaml:"connections_limit,omitempty"`
	// MaxRequestBodySizeBytes is the maximum allowed request body size.
	MaxRequestBodySizeBytes *int64 `yaml:"max_request_body_size_bytes"`
	// TruncateRequestBodyTooLarge truncates oversized bodies instead of returning an error.
	TruncateRequestBodyTooLarge bool `yaml:"truncate_request_body_too_large"`
	// ReadHeaderTimeout is the amount of time allowed to read request headers.
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout,omitempty"`
	// Protocol selects the protocol served by this listener.
	Protocol string `yaml:"protocol,omitempty"`
	// ServeTLS indicates that this listener has at least one usable certificate.
	ServeTLS bool `yaml:"-"`
	// Active indicates whether the listener has a configured purpose.
	Active bool `yaml:"-"`
}

// Lookup maps listener names to their options.
type Lookup map[string]*Options

// IsSupportedProtocol reports whether protocol is currently supported.
func IsSupportedProtocol(protocol string) bool {
	_, ok := supportedProtocols[protocol]
	return ok
}

// New returns default options for name.
func New(name string) *Options {
	o := FromFrontend(frontend.New())
	o.Protocol = ProtocolHTTP
	switch name {
	case DefaultFrontendName:
		o.Active = true
	case mgmt.ListenerNameMgmt:
		o.ListenAddress = mgmt.DefaultAddress
		o.ListenPort = mgmt.DefaultPort
		o.TLSListenPort = 0
		o.Active = true
	case mgmt.ListenerNameMetrics:
		o.ListenAddress = metrics.DefaultMetricsListenAddress
		o.ListenPort = metrics.DefaultMetricsListenPort
		o.TLSListenPort = 0
		o.Active = true
	default:
		o.ListenPort = 0
		o.TLSListenPort = 0
	}
	return o
}

// FromFrontend copies legacy frontend options into a standalone listener configuration.
func FromFrontend(source *frontend.Options) *Options {
	o := &Options{}
	if source == nil {
		return o
	}
	o.ListenAddress = source.ListenAddress
	o.ListenPort = source.ListenPort
	o.TLSListenAddress = source.TLSListenAddress
	o.TLSListenPort = source.TLSListenPort
	o.ConnectionsLimit = source.ConnectionsLimit
	if source.MaxRequestBodySizeBytes != nil {
		o.MaxRequestBodySizeBytes = new(*source.MaxRequestBodySizeBytes)
	}
	o.TruncateRequestBodyTooLarge = source.TruncateRequestBodyTooLarge
	o.ReadHeaderTimeout = source.ReadHeaderTimeout
	o.ServeTLS = source.ServeTLS
	return o
}

// FrontendOptions returns a frontend-compatible copy of these options.
func (o *Options) FrontendOptions() *frontend.Options {
	if o == nil {
		return nil
	}
	out := &frontend.Options{
		ListenAddress:               o.ListenAddress,
		ListenPort:                  o.ListenPort,
		TLSListenAddress:            o.TLSListenAddress,
		TLSListenPort:               o.TLSListenPort,
		ConnectionsLimit:            o.ConnectionsLimit,
		TruncateRequestBodyTooLarge: o.TruncateRequestBodyTooLarge,
		ReadHeaderTimeout:           o.ReadHeaderTimeout,
		ServeTLS:                    o.ServeTLS,
	}
	if o.MaxRequestBodySizeBytes != nil {
		out.MaxRequestBodySizeBytes = new(*o.MaxRequestBodySizeBytes)
	}
	return out
}

// NewLookup returns the three built-in listeners with their defaults.
func NewLookup() Lookup {
	return Lookup{
		DefaultFrontendName:      New(DefaultFrontendName),
		mgmt.ListenerNameMgmt:    New(mgmt.ListenerNameMgmt),
		mgmt.ListenerNameMetrics: New(mgmt.ListenerNameMetrics),
	}
}

// Clone returns a deep copy of the lookup.
func (l Lookup) Clone() Lookup {
	out := make(Lookup, len(l))
	for name, options := range l {
		if options != nil {
			out[name] = options.Clone()
		}
	}
	return out
}

// Clone returns a deep copy of the options.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	if o.MaxRequestBodySizeBytes != nil {
		out.MaxRequestBodySizeBytes = new(*o.MaxRequestBodySizeBytes)
	}
	return &out
}

// Equal reports whether both options have the same runtime configuration.
func (o *Options) Equal(other *Options) bool {
	if o == nil || other == nil {
		return o == other
	}
	if o.Protocol != other.Protocol || o.Active != other.Active ||
		o.ListenAddress != other.ListenAddress || o.ListenPort != other.ListenPort ||
		o.TLSListenAddress != other.TLSListenAddress || o.TLSListenPort != other.TLSListenPort ||
		o.ConnectionsLimit != other.ConnectionsLimit ||
		o.TruncateRequestBodyTooLarge != other.TruncateRequestBodyTooLarge ||
		o.ReadHeaderTimeout != other.ReadHeaderTimeout || o.ServeTLS != other.ServeTLS {
		return false
	}
	if o.MaxRequestBodySizeBytes == nil || other.MaxRequestBodySizeBytes == nil {
		return o.MaxRequestBodySizeBytes == other.MaxRequestBodySizeBytes
	}
	return *o.MaxRequestBodySizeBytes == *other.MaxRequestBodySizeBytes
}

// UnmarshalYAML overlays configured listeners onto the built-in defaults.
func (l *Lookup) UnmarshalYAML(unmarshal func(any) error) error {
	raw := make(map[string]yaml.MapSlice)
	if err := unmarshal(&raw); err != nil {
		return err
	}
	out := NewLookup()
	for name, values := range raw {
		o := New(name)
		data, err := yaml.Marshal(values)
		if err != nil {
			return fmt.Errorf("marshal listener %q: %w", name, err)
		}
		if err := yaml.Unmarshal(data, o); err != nil {
			return fmt.Errorf("unmarshal listener %q: %w", name, err)
		}
		if o.Protocol == "" {
			o.Protocol = ProtocolHTTP
		}
		out[name] = o
	}
	*l = out
	return nil
}
