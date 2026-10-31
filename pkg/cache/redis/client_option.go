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

package redis

import (
	"context"
	"crypto/tls"
	"net"
	"runtime"
	"slices"
	"time"

	redisopts "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	valkeyopts "github.com/trickstercache/trickster/v2/pkg/cache/valkey/options"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/compat"

	"github.com/valkey-io/valkey-go"
)

// these defaults are the ones the redis provider had before it moved to valkey-go, so that a
// redis cache keeps its connection counts, timeouts and retry behavior
const (
	defaultDialTimeout     = 5 * time.Second
	defaultConnTimeout     = 3 * time.Second
	defaultMaxRetries      = 3
	defaultMinRetryBackoff = 8 * time.Millisecond
	defaultMaxRetryBackoff = 512 * time.Millisecond
	defaultIdleTimeout     = 30 * time.Minute
	poolSizePerProc        = 10

	// keepAliveInterval is how often an idle pipelined connection is PINGed to find a dead peer
	keepAliveInterval = time.Second
	// singlePipelineConn is valkey-go's PipelineMultiplex for exactly one pipelined connection
	singlePipelineConn = -1
)

// clientOption maps a connection's options onto valkey-go's. A nil s is the redis provider's
// classic profile: a pooled connection per command, as go-redis made, and no Valkey-only feature.
func clientOption(ro *redisopts.Options, s *valkeyopts.Settings) (valkey.ClientOption, error) {
	o := valkey.ClientOption{
		Username: ro.Username,
		Password: string(ro.Password),
		Dialer: net.Dialer{
			Timeout:   positiveOr(time.Duration(ro.DialTimeout), defaultDialTimeout),
			KeepAlive: keepAliveInterval,
		},
		ConnWriteTimeout:      connTimeout(ro),
		ConnLifetime:          time.Duration(ro.ConnMaxLifetime),
		BlockingPoolSize:      positiveOr(ro.PoolSize, poolSizePerProc*runtime.GOMAXPROCS(0)),
		BlockingPoolMinSize:   ro.MinIdleConns,
		BlockingPoolCleanup:   positiveOr(time.Duration(ro.ConnMaxIdleTime), defaultIdleTimeout),
		DisableCache:          true,
		DisableAutoPipelining: true,
	}
	switch ro.ClientType {
	case redisopts.ClientTypeCluster:
		if len(ro.Endpoints) == 0 {
			return o, ErrInvalidEndpointsConfig
		}
		o.InitAddress = slices.Clone(ro.Endpoints)
	case redisopts.ClientTypeSentinel:
		if len(ro.Endpoints) == 0 {
			return o, ErrInvalidEndpointsConfig
		}
		if ro.SentinelMaster == "" {
			return o, ErrInvalidSentinalMasterConfig
		}
		o.InitAddress = slices.Clone(ro.Endpoints)
		o.Sentinel.MasterSet = ro.SentinelMaster
		o.SelectDB = ro.DB
	default:
		if ro.Endpoint == "" {
			return o, ErrInvalidEndpointConfig
		}
		o.InitAddress = []string{ro.Endpoint}
		o.ForceSingleClient = true
		o.SelectDB = ro.DB
	}
	if ro.UseTLS {
		o.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		o.Sentinel.TLSConfig = o.TLSConfig
	}
	switch ro.Protocol {
	case "", redisopts.ProtocolTCP:
	case redisopts.ProtocolUnix:
		o.DialCtxFn = dialUnix
	default:
		return o, ErrInvalidProtocolConfig
	}
	if ro.MaxRetries < 0 {
		o.DisableRetry = true
	} else {
		o.RetryDelay = retryDelay(positiveOr(ro.MaxRetries, defaultMaxRetries),
			positiveOr(time.Duration(ro.MinRetryBackoff), defaultMinRetryBackoff),
			positiveOr(time.Duration(ro.MaxRetryBackoff), defaultMaxRetryBackoff))
	}
	if s != nil {
		applySettings(&o, ro, s)
	}
	return o, nil
}

// applySettings turns on the valkey provider's settings
func applySettings(o *valkey.ClientOption, ro *redisopts.Options, s *valkeyopts.Settings) {
	o.DisableAutoPipelining = !s.AutoPipelining
	if s.PipelineMultiplex != nil {
		// valkey-go reads 0 as its own pick, and a negative value as one connection
		o.PipelineMultiplex = *s.PipelineMultiplex
		if o.PipelineMultiplex == 0 {
			o.PipelineMultiplex = singlePipelineConn
		}
	}
	o.MaxFlushDelay = time.Duration(s.MaxFlushDelay)
	o.ReadBufferEachConn = s.ReadBufferBytes
	o.WriteBufferEachConn = s.WriteBufferBytes
	// a Valkey 9 cluster can serve numbered databases, where Redis Cluster rejects SELECT
	if ro.ClientType == redisopts.ClientTypeCluster {
		o.SelectDB = ro.DB
	}
	applyReplicaReads(o, &s.ReplicaReads)
	if cc := &s.ClientSideCache; cc.Enabled {
		o.DisableCache = false
		o.CacheSizeEachConn = cc.SizeBytesPerConnection
		if cc.Mode == valkeyopts.ClientCacheModeBroadcast {
			o.ClientTrackingOptions = broadcastTracking(cc.Prefixes)
		}
	}
}

func applyReplicaReads(o *valkey.ClientOption, r *valkeyopts.ReplicaReadOptions) {
	az := string(r.AvailabilityZone)
	switch r.Mode {
	case valkeyopts.ReplicaReadsPreferReplica:
		o.ReadNodeSelector = valkey.PreferReplicaNodeSelector()
	case valkeyopts.ReplicaReadsAZAffinity:
		o.EnableReplicaAZInfo = true
		o.ReadNodeSelector = valkey.AZAffinityNodeSelector(az)
	case valkeyopts.ReplicaReadsAZAffinityWithPrimary:
		o.EnableReplicaAZInfo = true
		o.ReadNodeSelector = valkey.AZAffinityReplicasAndPrimaryNodeSelector(az)
	}
	if len(r.ReplicaEndpoints) > 0 {
		o.Standalone.ReplicaAddress = slices.Clone(r.ReplicaEndpoints)
	}
	if o.ReadNodeSelector != nil || len(o.Standalone.ReplicaAddress) > 0 {
		o.SendToReplicas = readOnly
	}
}

// readOnly sends the cache's reads, GETs, to replicas, and its writes to the primary
func readOnly(cmd valkey.Completed) bool {
	return cmd.IsReadOnly()
}

// broadcastTracking returns the CLIENT TRACKING arguments that track every key under prefixes
func broadcastTracking(prefixes []string) []string {
	out := make([]string, 0, len(prefixes)*2+1)
	for _, p := range prefixes {
		out = append(out, trackingPrefix, p)
	}
	return append(out, trackingBroadcast)
}

const (
	trackingPrefix    = "PREFIX"
	trackingBroadcast = "BCAST"
	networkUnix       = "unix"
)

func dialUnix(ctx context.Context, dst string, d *net.Dialer, cfg *tls.Config) (net.Conn, error) {
	if cfg != nil {
		td := tls.Dialer{NetDialer: d, Config: cfg}
		return td.DialContext(ctx, networkUnix, dst)
	}
	return d.DialContext(ctx, networkUnix, dst)
}

// connTimeout bounds a command's wait on its connection, and how long a keepalive PING may take
// before its connection is dropped. go-redis's default write timeout was its read timeout.
func connTimeout(ro *redisopts.Options) time.Duration {
	return positiveOr(max(time.Duration(ro.ReadTimeout), time.Duration(ro.WriteTimeout)), defaultConnTimeout)
}

// retryDelay retries a read up to maxRetries times, backing off exponentially with jitter
// between minBackoff and maxBackoff, as go-redis did
func retryDelay(maxRetries int, minBackoff, maxBackoff time.Duration) valkey.RetryDelayFn {
	return func(attempts int, _ valkey.Completed, _ error) time.Duration {
		if attempts > maxRetries {
			return -1
		}
		d := minBackoff << min(attempts-1, 30)
		if d <= 0 || d > maxBackoff {
			d = maxBackoff
		}
		return min(minBackoff+time.Duration(compat.Int64()%int64(d)), maxBackoff)
	}
}

func positiveOr[T int | time.Duration](v, fallback T) T {
	if v > 0 {
		return v
	}
	return fallback
}
