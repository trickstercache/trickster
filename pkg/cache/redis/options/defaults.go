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

const (
	// ClientTypeStandard connects to a single server
	ClientTypeStandard = "standard"
	// ClientTypeCluster connects to a cluster through its seed endpoints
	ClientTypeCluster = "cluster"
	// ClientTypeSentinel connects to the primary that a set of sentinels names
	ClientTypeSentinel = "sentinel"

	// ProtocolTCP dials endpoints over TCP
	ProtocolTCP = "tcp"
	// ProtocolUnix dials endpoints as Unix socket paths
	ProtocolUnix = "unix"

	// KeyPoolSize is the YAML key of PoolSize
	KeyPoolSize = "pool_size"
	// KeyMinIdleConns is the YAML key of MinIdleConns
	KeyMinIdleConns = "min_idle_conns"
	// KeyIdleTimeout is the YAML key of ConnMaxIdleTime
	KeyIdleTimeout = "idle_timeout"
	// KeyPoolTimeout is the YAML key of PoolTimeout
	KeyPoolTimeout = "pool_timeout"

	// DefaultRedisClientType is the default Redis Client Type
	DefaultRedisClientType = ClientTypeStandard
	// DefaultRedisProtocol is the default Redis Client protocol
	DefaultRedisProtocol = ProtocolTCP
	// DefaultRedisEndpoint is the default Redis Client endpoint
	DefaultRedisEndpoint = "redis:6379"
)
