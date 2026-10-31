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

import (
	"strings"
	"testing"
	"time"

	redisopts "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const fullYAML = `
client_type: cluster
endpoints: [v0:6379, v1:6379]
username: u
password: p
db: 3
pool_size: 40
auto_pipelining: true
pipeline_multiplex: 0
max_flush_delay: 20us
read_buffer_bytes: 1024
write_buffer_bytes: 2048
replica_reads:
  mode: az_affinity
  availability_zone: us-east-1a
client_side_cache:
  enabled: true
  mode: broadcast
  prefixes: [a, b]
  size_bytes_per_connection: 4096
  max_ttl: 5s
`

func TestNewDefaults(t *testing.T) {
	t.Parallel()
	o := New()
	require.Equal(t, DefaultValkeyEndpoint, o.Endpoint)
	require.Equal(t, []string{DefaultValkeyEndpoint}, o.Endpoints)
	require.Equal(t, redisopts.ClientTypeStandard, o.ClientType)
	require.False(t, o.AutoPipelining)
	require.Nil(t, o.PipelineMultiplex)
	require.Equal(t, ReplicaReadsNone, o.ReplicaReads.Mode)
	require.False(t, o.ClientSideCache.Enabled)
	require.Equal(t, DefaultClientCacheSizeBytesPerConnection, o.ClientSideCache.SizeBytesPerConnection)
	require.NoError(t, o.Validate())
}

func TestUnmarshalYAML(t *testing.T) {
	t.Parallel()
	o := &Options{}
	require.NoError(t, yaml.Unmarshal([]byte(fullYAML), o))
	require.Equal(t, redisopts.ClientTypeCluster, o.ClientType)
	require.Equal(t, []string{"v0:6379", "v1:6379"}, o.Endpoints)
	require.Equal(t, "u", o.Username)
	require.Equal(t, 3, o.DB)
	require.Equal(t, 40, o.PoolSize)
	// the settings survive the embedded type's decode
	require.True(t, o.AutoPipelining)
	require.NotNil(t, o.PipelineMultiplex)
	require.Equal(t, 0, *o.PipelineMultiplex)
	require.Equal(t, timeconv.Duration(20*time.Microsecond), o.MaxFlushDelay)
	require.Equal(t, 1024, o.ReadBufferBytes)
	require.Equal(t, 2048, o.WriteBufferBytes)
	require.Equal(t, ReplicaReadsAZAffinity, o.ReplicaReads.Mode)
	require.Equal(t, "us-east-1a", string(o.ReplicaReads.AvailabilityZone))
	require.True(t, o.ClientSideCache.Enabled)
	require.Equal(t, []string{"a", "b"}, o.ClientSideCache.Prefixes)
	require.Equal(t, timeconv.Duration(5*time.Second), o.ClientSideCache.MaxTTL)
	require.NoError(t, o.Validate())

	// keys left out keep the valkey defaults, not the redis ones
	p := &Options{}
	require.NoError(t, yaml.Unmarshal([]byte("db: 1\n"), p))
	require.Equal(t, DefaultValkeyEndpoint, p.Endpoint)
	require.False(t, p.AutoPipelining)
	require.Equal(t, ClientCacheModeOptIn, p.ClientSideCache.Mode)
	require.Equal(t, timeconv.Duration(DefaultClientCacheMaxTTL), p.ClientSideCache.MaxTTL)

	require.Error(t, yaml.Unmarshal([]byte("db: [x]\n"), &Options{}))
	require.Error(t, yaml.Unmarshal([]byte("auto_pipelining: [x]\n"), &Options{}))
}

func TestMarshalRoundTrip(t *testing.T) {
	t.Parallel()
	o := &Options{}
	require.NoError(t, yaml.Unmarshal([]byte(fullYAML), o))
	out, err := yaml.Marshal(o)
	require.NoError(t, err)
	// embedded and Valkey-only keys print side by side under one mapping
	for _, k := range []string{"\nclient_type: cluster", "\nauto_pipelining: true", "\npipeline_multiplex: 0",
		"\nreplica_reads:", "\nclient_side_cache:"} {
		require.True(t, strings.Contains("\n"+string(out), k), "missing %q in\n%s", k, out)
	}
	o2 := &Options{}
	require.NoError(t, yaml.Unmarshal(out, o2))
	require.True(t, o.Equal(o2))
}

func TestEqualAndClone(t *testing.T) {
	t.Parallel()
	o := &Options{}
	require.NoError(t, yaml.Unmarshal([]byte(fullYAML), o))
	c := o.Clone()
	require.True(t, o.Equal(c))
	require.True(t, (*Options)(nil).Equal(nil))
	require.False(t, o.Equal(nil))
	require.Nil(t, (*Options)(nil).Clone())

	// a clone shares no slices or pointers with its source
	c.Endpoints[0] = "x"
	c.ClientSideCache.Prefixes[0] = "x"
	*c.PipelineMultiplex = 5
	require.Equal(t, "v0:6379", o.Endpoints[0])
	require.Equal(t, "a", o.ClientSideCache.Prefixes[0])
	require.Equal(t, 0, *o.PipelineMultiplex)

	muts := []func(*Options){
		func(o *Options) { o.Endpoint = "other" },
		func(o *Options) { o.AutoPipelining = !o.AutoPipelining },
		func(o *Options) { o.PipelineMultiplex = nil },
		func(o *Options) { o.MaxFlushDelay++ },
		func(o *Options) { o.ReadBufferBytes++ },
		func(o *Options) { o.WriteBufferBytes++ },
		func(o *Options) { o.ReplicaReads.Mode = ReplicaReadsNone },
		func(o *Options) { o.ReplicaReads.AvailabilityZone = "z" },
		func(o *Options) { o.ReplicaReads.ReplicaEndpoints = []string{"r"} },
		func(o *Options) { o.ClientSideCache.Enabled = false },
		func(o *Options) { o.ClientSideCache.Mode = ClientCacheModeOptIn },
		func(o *Options) { o.ClientSideCache.Prefixes = nil },
		func(o *Options) { o.ClientSideCache.SizeBytesPerConnection++ },
		func(o *Options) { o.ClientSideCache.MaxTTL++ },
	}
	for i, mut := range muts {
		m := o.Clone()
		mut(m)
		require.False(t, o.Equal(m), "mutation %d", i)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	over := MaxPipelineMultiplex + 1
	neg := -1
	tests := []struct {
		name string
		mut  func(*Options)
		err  error
	}{
		{"multiplex over", func(o *Options) { o.PipelineMultiplex = &over }, ErrInvalidPipelineMultiplex},
		{"multiplex negative", func(o *Options) { o.PipelineMultiplex = &neg }, ErrInvalidPipelineMultiplex},
		{"negative buffer", func(o *Options) { o.ReadBufferBytes = -1 }, ErrNegativeSetting},
		{"bad replica mode", func(o *Options) { o.ReplicaReads.Mode = "x" }, ErrInvalidReplicaReadsMode},
		{"az without zone", func(o *Options) { o.ReplicaReads.Mode = ReplicaReadsAZAffinityWithPrimary }, ErrAvailabilityZoneRequired},
		{"replica endpoints on cluster", func(o *Options) {
			o.ClientType = redisopts.ClientTypeCluster
			o.ReplicaReads.ReplicaEndpoints = []string{"r:6379"}
		}, ErrReplicaEndpointsClient},
		{"bad cache mode", func(o *Options) { o.ClientSideCache.Mode = "x" }, ErrInvalidClientCacheMode},
		{"prefixes without broadcast", func(o *Options) { o.ClientSideCache.Prefixes = []string{"a"} }, ErrPrefixesNeedBroadcast},
		{"negative cache size", func(o *Options) { o.ClientSideCache.SizeBytesPerConnection = -1 }, ErrNegativeClientCache},
		{"replica endpoints on standard", func(o *Options) { o.ReplicaReads.ReplicaEndpoints = []string{"r:6379"} }, nil},
		{"az with zone", func(o *Options) {
			o.ReplicaReads.Mode = ReplicaReadsAZAffinity
			o.ReplicaReads.AvailabilityZone = "a"
		}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := New()
			test.mut(o)
			require.ErrorIs(t, o.Validate(), test.err)
		})
	}
}

func TestReadFeatures(t *testing.T) {
	t.Parallel()
	o := New()
	require.False(t, o.ReadsReplicas())
	o.ReplicaReads.ReplicaEndpoints = []string{"r:6379"}
	require.True(t, o.ReadsReplicas())
	o.ReplicaReads = ReplicaReadOptions{Mode: ReplicaReadsPreferReplica}
	o.ClientSideCache.Enabled = true
	require.True(t, o.ReadsReplicas())
	w := o.WithoutReadFeatures()
	require.False(t, w.ReadsReplicas())
	require.False(t, w.ClientSideCache.Enabled)
	require.True(t, o.ClientSideCache.Enabled)
}
