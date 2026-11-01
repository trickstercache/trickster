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

// Package options provides the configuration of the valkey cache provider, which takes every
// redis option and adds the settings only a Valkey server supports
package options

import (
	"errors"
	"fmt"
	"slices"

	redisopts "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"go.yaml.in/yaml/v3"
)

// Options configures a valkey cache: its connection, as for redis, and its Valkey-only Settings
type Options struct {
	redisopts.Options `yaml:",inline"`
	Settings          `yaml:",inline"`
}

// Settings are the options only the valkey provider has. It must not implement yaml.Unmarshaler,
// since Options decodes it from the same mapping as the embedded redis options.
type Settings struct {
	// AutoPipelining shares connections among concurrent commands; when false, each command
	// takes a pooled connection of its own, as with the redis provider
	AutoPipelining bool `yaml:"auto_pipelining"`
	// PipelineMultiplex is the log2 of the pipelined connections to each node (0 is one connection);
	// when unset, the client picks it: up to 4 for a single server, and 1 for each cluster node
	PipelineMultiplex *int `yaml:"pipeline_multiplex,omitempty"`
	// MaxFlushDelay pauses the pipeline's write loop up to this long, to send more commands per write
	MaxFlushDelay timeconv.Duration `yaml:"max_flush_delay,omitempty"`
	// ReadBufferBytes is the read buffer size of each connection, or the client's default when 0
	ReadBufferBytes int `yaml:"read_buffer_bytes,omitempty"`
	// WriteBufferBytes is the write buffer size of each connection, or the client's default when 0
	WriteBufferBytes int `yaml:"write_buffer_bytes,omitempty"`
	// ReplicaReads routes cache reads to replicas
	ReplicaReads ReplicaReadOptions `yaml:"replica_reads,omitempty"`
	// ClientSideCache keeps recently read objects in this process, invalidated by the server
	ClientSideCache ClientSideCacheOptions `yaml:"client_side_cache,omitempty"`
}

// ReplicaReadOptions configures where cache reads are sent
type ReplicaReadOptions struct {
	// Mode is none, prefer_replica, az_affinity or az_affinity_with_primary
	Mode string `yaml:"mode,omitempty"`
	// AvailabilityZone is this instance's zone, which the az_affinity modes prefer
	AvailabilityZone string `yaml:"availability_zone,omitempty"`
	// ReplicaEndpoints lists read replicas of a standard (single server) client
	ReplicaEndpoints []string `yaml:"replica_endpoints,omitempty"`
}

// ClientSideCacheOptions configures server-assisted caching in this process
type ClientSideCacheOptions struct {
	// Enabled turns on the client-side cache
	Enabled bool `yaml:"enabled,omitempty"`
	// Mode is optin, tracking the keys read through the cache, or broadcast, tracking prefixes
	Mode string `yaml:"mode,omitempty"`
	// Prefixes are the key prefixes the server tracks in broadcast mode
	Prefixes []string `yaml:"prefixes,omitempty"`
	// SizeBytesPerConnection is the cache budget of each connection to each node
	SizeBytesPerConnection int `yaml:"size_bytes_per_connection,omitempty"`
	// MaxTTL caps how long an entry is kept; the server's TTL for the key can shorten it
	MaxTTL timeconv.Duration `yaml:"max_ttl,omitempty"`
}

var (
	ErrInvalidPipelineMultiplex = fmt.Errorf("pipeline_multiplex must be between 0 and %d", MaxPipelineMultiplex)
	ErrNegativeSetting          = errors.New("max_flush_delay, read_buffer_bytes and write_buffer_bytes can't be negative")
	ErrInvalidReplicaReadsMode  = errors.New("invalid replica_reads mode")
	ErrAvailabilityZoneRequired = errors.New("replica_reads az_affinity modes require availability_zone")
	ErrReplicaEndpointsClient   = errors.New("replica_reads replica_endpoints requires client_type standard")
	ErrInvalidClientCacheMode   = errors.New("invalid client_side_cache mode")
	ErrPrefixesNeedBroadcast    = errors.New("client_side_cache prefixes require mode broadcast")
	ErrNegativeClientCache      = errors.New("client_side_cache size_bytes_per_connection and max_ttl can't be negative")
)

// New returns Options with the valkey provider's defaults
func New() *Options {
	return &Options{
		ClientType:     redisopts.DefaultRedisClientType,
		Protocol:       redisopts.DefaultRedisProtocol,
		Endpoint:       DefaultValkeyEndpoint,
		Endpoints:      []string{DefaultValkeyEndpoint},
		AutoPipelining: DefaultAutoPipelining,
		ReplicaReads:   ReplicaReadOptions{Mode: ReplicaReadsNone},
		ClientSideCache: ClientSideCacheOptions{
			Mode:                   ClientCacheModeOptIn,
			SizeBytesPerConnection: DefaultClientCacheSizeBytesPerConnection,
			MaxTTL:                 timeconv.Duration(DefaultClientCacheMaxTTL),
		},
	}
}

// UnmarshalYAML decodes the embedded redis options and the Settings from the same mapping. It must
// exist: Go would otherwise promote the redis options' UnmarshalYAML, which drops the Settings.
func (o *Options) UnmarshalYAML(value *yaml.Node) error {
	n := New()
	if err := n.Options.DecodeYAML(value); err != nil {
		return err
	}
	if err := value.Decode(&n.Settings); err != nil {
		return err
	}
	*o = *n
	return nil
}

// MarshalYAML writes the embedded redis options and the Settings as one mapping. yaml.v3 leaves an
// inlined type out of its field list when that type has an UnmarshalYAML, so the redis keys need this.
func (o Options) MarshalYAML() (any, error) {
	type plainRedis redisopts.Options
	return struct {
		plainRedis `yaml:",inline"`
		Settings   `yaml:",inline"`
	}{plainRedis(o.Options), o.Settings}, nil
}

// Equal returns true if all values in the Options are identical
func (o *Options) Equal(o2 *Options) bool {
	if o == nil || o2 == nil {
		return o == o2
	}
	return o.Options.Equal(&o2.Options) && o.Settings.equal(&o2.Settings)
}

func (s *Settings) equal(s2 *Settings) bool {
	return s.AutoPipelining == s2.AutoPipelining &&
		equalIntPointers(s.PipelineMultiplex, s2.PipelineMultiplex) &&
		s.MaxFlushDelay == s2.MaxFlushDelay &&
		s.ReadBufferBytes == s2.ReadBufferBytes &&
		s.WriteBufferBytes == s2.WriteBufferBytes &&
		s.ReplicaReads.Mode == s2.ReplicaReads.Mode &&
		s.ReplicaReads.AvailabilityZone == s2.ReplicaReads.AvailabilityZone &&
		slices.Equal(s.ReplicaReads.ReplicaEndpoints, s2.ReplicaReads.ReplicaEndpoints) &&
		s.ClientSideCache.Enabled == s2.ClientSideCache.Enabled &&
		s.ClientSideCache.Mode == s2.ClientSideCache.Mode &&
		slices.Equal(s.ClientSideCache.Prefixes, s2.ClientSideCache.Prefixes) &&
		s.ClientSideCache.SizeBytesPerConnection == s2.ClientSideCache.SizeBytesPerConnection &&
		s.ClientSideCache.MaxTTL == s2.ClientSideCache.MaxTTL
}

func equalIntPointers(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Clone returns a deep copy of the Options
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	out.Options = *o.Options.Clone()
	if o.PipelineMultiplex != nil {
		n := *o.PipelineMultiplex
		out.PipelineMultiplex = &n
	}
	out.ReplicaReads.ReplicaEndpoints = slices.Clone(o.ReplicaReads.ReplicaEndpoints)
	out.ClientSideCache.Prefixes = slices.Clone(o.ClientSideCache.Prefixes)
	return &out
}

// Validate returns an error when a Valkey-only setting is out of range or contradicts another
func (o *Options) Validate() error {
	s := &o.Settings
	if s.PipelineMultiplex != nil && (*s.PipelineMultiplex < 0 || *s.PipelineMultiplex > MaxPipelineMultiplex) {
		return ErrInvalidPipelineMultiplex
	}
	if s.MaxFlushDelay < 0 || s.ReadBufferBytes < 0 || s.WriteBufferBytes < 0 {
		return ErrNegativeSetting
	}
	switch s.ReplicaReads.Mode {
	case "", ReplicaReadsNone, ReplicaReadsPreferReplica:
	case ReplicaReadsAZAffinity, ReplicaReadsAZAffinityWithPrimary:
		if s.ReplicaReads.AvailabilityZone == "" {
			return ErrAvailabilityZoneRequired
		}
	default:
		return ErrInvalidReplicaReadsMode
	}
	if len(s.ReplicaReads.ReplicaEndpoints) > 0 && o.ClientType != redisopts.ClientTypeStandard {
		return ErrReplicaEndpointsClient
	}
	switch s.ClientSideCache.Mode {
	case "", ClientCacheModeOptIn:
		if len(s.ClientSideCache.Prefixes) > 0 {
			return ErrPrefixesNeedBroadcast
		}
	case ClientCacheModeBroadcast:
	default:
		return ErrInvalidClientCacheMode
	}
	if s.ClientSideCache.SizeBytesPerConnection < 0 || s.ClientSideCache.MaxTTL < 0 {
		return ErrNegativeClientCache
	}
	return nil
}

// ReadsReplicas reports whether any cache read may be served by a replica
func (s *Settings) ReadsReplicas() bool {
	return (s.ReplicaReads.Mode != "" && s.ReplicaReads.Mode != ReplicaReadsNone) ||
		len(s.ReplicaReads.ReplicaEndpoints) > 0
}

// WithoutReadFeatures returns a copy whose reads always reach the primary, with no client-side
// cache, for a client that needs every read to see the latest write, such as a lock holder
func (o *Options) WithoutReadFeatures() *Options {
	out := o.Clone()
	out.ReplicaReads = ReplicaReadOptions{Mode: ReplicaReadsNone}
	out.ClientSideCache.Enabled = false
	return out
}
