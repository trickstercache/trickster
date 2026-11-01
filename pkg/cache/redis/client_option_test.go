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
	"path/filepath"
	"runtime"
	"testing"
	"time"

	ro "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	vo "github.com/trickstercache/trickster/v2/pkg/cache/valkey/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"github.com/valkey-io/valkey-go"
)

func TestClientOptionClassicDefaults(t *testing.T) {
	o, err := clientOption(&ro.Options{ClientType: ro.ClientTypeStandard, Endpoint: "e:6379", DB: 2}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"e:6379"}, o.InitAddress)
	require.True(t, o.ForceSingleClient)
	require.Equal(t, 2, o.SelectDB)
	// the classic profile pools a connection per command, sized and timed as go-redis was
	require.True(t, o.DisableAutoPipelining)
	require.True(t, o.DisableCache)
	require.Equal(t, poolSizePerProc*runtime.GOMAXPROCS(0), o.BlockingPoolSize)
	require.Equal(t, defaultIdleTimeout, o.BlockingPoolCleanup)
	require.Equal(t, defaultDialTimeout, o.Dialer.Timeout)
	require.Equal(t, keepAliveInterval, o.Dialer.KeepAlive)
	require.Equal(t, defaultConnTimeout, o.ConnWriteTimeout)
	require.False(t, o.DisableRetry)
	require.NotNil(t, o.RetryDelay)
	require.Nil(t, o.TLSConfig)
	require.Nil(t, o.DialCtxFn)
}

func TestClientOptionMapping(t *testing.T) {
	r := &ro.Options{
		ClientType: ro.ClientTypeSentinel, Endpoints: []string{"s1:26379", "s2:26379"}, SentinelMaster: "m",
		Username: "u", Password: "p", DB: 4, UseTLS: true, MaxRetries: -1, PoolSize: 7, MinIdleConns: 2,
		DialTimeout:     timeconv.Duration(time.Second),
		ReadTimeout:     timeconv.Duration(2 * time.Second),
		WriteTimeout:    timeconv.Duration(4 * time.Second),
		ConnMaxLifetime: timeconv.Duration(time.Hour),
		ConnMaxIdleTime: timeconv.Duration(time.Minute),
	}
	o, err := clientOption(r, nil)
	require.NoError(t, err)
	require.Equal(t, r.Endpoints, o.InitAddress)
	require.Equal(t, "m", o.Sentinel.MasterSet)
	require.False(t, o.ForceSingleClient)
	require.Equal(t, "u", o.Username)
	require.Equal(t, "p", o.Password)
	require.Equal(t, 4, o.SelectDB)
	require.Equal(t, uint16(tls.VersionTLS12), o.TLSConfig.MinVersion)
	require.Same(t, o.TLSConfig, o.Sentinel.TLSConfig)
	require.True(t, o.DisableRetry)
	require.Equal(t, 7, o.BlockingPoolSize)
	require.Equal(t, 2, o.BlockingPoolMinSize)
	require.Equal(t, time.Minute, o.BlockingPoolCleanup)
	require.Equal(t, time.Hour, o.ConnLifetime)
	require.Equal(t, time.Second, o.Dialer.Timeout)
	// the connection timeout is the longer of the read and write timeouts
	require.Equal(t, 4*time.Second, o.ConnWriteTimeout)
	o.InitAddress[0] = "changed"
	require.Equal(t, "s1:26379", r.Endpoints[0])

	// a classic cluster ignores db, which Redis Cluster rejects
	o, err = clientOption(&ro.Options{ClientType: ro.ClientTypeCluster, Endpoints: []string{"c:6379"}, DB: 3}, nil)
	require.NoError(t, err)
	require.Zero(t, o.SelectDB)
	require.False(t, o.ForceSingleClient)

	o, err = clientOption(&ro.Options{Endpoint: "/tmp/v.sock", Protocol: ro.ProtocolUnix}, nil)
	require.NoError(t, err)
	require.NotNil(t, o.DialCtxFn)
}

func TestApplySettings(t *testing.T) {
	r := &ro.Options{ClientType: ro.ClientTypeCluster, Endpoints: []string{"c:6379"}, DB: 3}
	zero, three := 0, 3
	s := &vo.New().Settings
	s.AutoPipelining = true
	s.PipelineMultiplex = &zero
	s.MaxFlushDelay = timeconv.Duration(20 * time.Microsecond)
	s.ReadBufferBytes, s.WriteBufferBytes = 1024, 2048
	o, err := clientOption(r, s)
	require.NoError(t, err)
	require.False(t, o.DisableAutoPipelining)
	require.Equal(t, singlePipelineConn, o.PipelineMultiplex)
	require.Equal(t, 20*time.Microsecond, o.MaxFlushDelay)
	require.Equal(t, 1024, o.ReadBufferEachConn)
	require.Equal(t, 2048, o.WriteBufferEachConn)
	require.Equal(t, 3, o.SelectDB)
	require.True(t, o.DisableCache)
	require.Nil(t, o.SendToReplicas)

	s.PipelineMultiplex = &three
	s.AutoPipelining = false
	s.ClientSideCache = vo.ClientSideCacheOptions{Enabled: true, Mode: vo.ClientCacheModeBroadcast,
		Prefixes: []string{"a", "b"}, SizeBytesPerConnection: 4096}
	o, err = clientOption(r, s)
	require.NoError(t, err)
	require.True(t, o.DisableAutoPipelining)
	require.Equal(t, 3, o.PipelineMultiplex)
	require.False(t, o.DisableCache)
	require.Equal(t, 4096, o.CacheSizeEachConn)
	require.Equal(t, []string{"PREFIX", "a", "PREFIX", "b", "BCAST"}, o.ClientTrackingOptions)

	s.ClientSideCache.Mode = vo.ClientCacheModeOptIn
	o, err = clientOption(r, s)
	require.NoError(t, err)
	require.Nil(t, o.ClientTrackingOptions)
}

func TestApplyReplicaReads(t *testing.T) {
	nodes := []valkey.NodeInfo{{Addr: "primary", AZ: "a"}, {Addr: "replica-a", AZ: "a"}, {Addr: "replica-b", AZ: "b"}}
	for _, test := range []struct {
		mode   string
		az     bool
		chosen string
	}{
		{vo.ReplicaReadsPreferReplica, false, ""},
		{vo.ReplicaReadsAZAffinity, true, "replica-b"},
		{vo.ReplicaReadsAZAffinityWithPrimary, true, ""},
	} {
		t.Run(test.mode, func(t *testing.T) {
			o := &valkey.ClientOption{}
			applyReplicaReads(o, &vo.ReplicaReadOptions{Mode: test.mode, AvailabilityZone: "b"})
			require.Equal(t, test.az, o.EnableReplicaAZInfo)
			require.NotNil(t, o.ReadNodeSelector)
			require.NotNil(t, o.SendToReplicas)
			if test.chosen != "" {
				require.Equal(t, test.chosen, nodes[o.ReadNodeSelector(0, nodes)].Addr)
			}
		})
	}
	o := &valkey.ClientOption{}
	applyReplicaReads(o, &vo.ReplicaReadOptions{Mode: vo.ReplicaReadsNone, ReplicaEndpoints: []string{"r:6379"}})
	require.Equal(t, []string{"r:6379"}, o.Standalone.ReplicaAddress)
	require.NotNil(t, o.SendToReplicas)
	o = &valkey.ClientOption{}
	applyReplicaReads(o, &vo.ReplicaReadOptions{Mode: vo.ReplicaReadsNone})
	require.Nil(t, o.SendToReplicas)
}

func TestReadOnly(t *testing.T) {
	c, closeServer := setupRedisCache(ro.ClientTypeStandard)
	defer closeServer()
	require.NoError(t, c.Connect())
	defer c.Close()
	cl, err := c.Holder().Client()
	require.NoError(t, err)
	require.True(t, readOnly(cl.B().Get().Key("k").Build()))
	require.False(t, readOnly(cl.B().Set().Key("k").Value("v").Build()))
}

func TestRetryDelay(t *testing.T) {
	fn := retryDelay(3, 8*time.Millisecond, 20*time.Millisecond)
	for attempt := 1; attempt <= 3; attempt++ {
		for range 50 {
			d := fn(attempt, valkey.Completed{}, nil)
			require.GreaterOrEqual(t, d, 8*time.Millisecond)
			require.LessOrEqual(t, d, 20*time.Millisecond)
		}
	}
	require.Negative(t, fn(4, valkey.Completed{}, nil))
	// a shift past the duration's range falls back to the maximum backoff
	require.LessOrEqual(t, retryDelay(100, time.Hour, 2*time.Hour)(64, valkey.Completed{}, nil), 2*time.Hour)
}

func TestDialUnix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen(networkUnix, path)
	require.NoError(t, err)
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	conn, err := dialUnix(context.Background(), path, &net.Dialer{}, nil)
	require.NoError(t, err)
	conn.Close()
	// a TLS handshake with a peer that hangs up fails
	_, err = dialUnix(context.Background(), path, &net.Dialer{}, &tls.Config{MinVersion: tls.VersionTLS12})
	require.Error(t, err)
}

func TestConnTimeout(t *testing.T) {
	require.Equal(t, defaultConnTimeout, connTimeout(&ro.Options{}))
	require.Equal(t, time.Second, connTimeout(&ro.Options{ReadTimeout: timeconv.Duration(time.Second)}))
}
