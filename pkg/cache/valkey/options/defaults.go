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

package options

import "time"

const (
	// DefaultValkeyEndpoint is the default Valkey endpoint
	DefaultValkeyEndpoint = "valkey:6379"
	// DefaultAutoPipelining is off, as a small read pipelined behind a multi-megabyte reply waits for it
	DefaultAutoPipelining = false
	// MaxPipelineMultiplex is the largest pipeline_multiplex, which is 256 connections per node
	MaxPipelineMultiplex = 8

	// ReplicaReadsNone sends every read to the primary
	ReplicaReadsNone = "none"
	// ReplicaReadsPreferReplica sends reads to a replica when the shard has one
	ReplicaReadsPreferReplica = "prefer_replica"
	// ReplicaReadsAZAffinity sends reads to a replica in the client's availability zone
	ReplicaReadsAZAffinity = "az_affinity"
	// ReplicaReadsAZAffinityWithPrimary sends reads to any node in the client's availability zone
	ReplicaReadsAZAffinityWithPrimary = "az_affinity_with_primary"

	// ClientCacheModeOptIn tracks only the keys the client reads through its cache
	ClientCacheModeOptIn = "optin"
	// ClientCacheModeBroadcast tracks every key under the configured prefixes
	ClientCacheModeBroadcast = "broadcast"

	// DefaultClientCacheSizeBytesPerConnection is the client-side cache budget of each connection
	DefaultClientCacheSizeBytesPerConnection = 16 << 20
	// DefaultClientCacheMaxTTL caps how long a client-side cache entry lives
	DefaultClientCacheMaxTTL = 30 * time.Second
)
