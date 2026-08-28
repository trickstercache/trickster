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

package template

import (
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"github.com/stretchr/testify/require"
)

func newTemplate(t *testing.T) *bo.Options {
	o := bo.New()
	o.Provider = "prometheus"
	o.IsTemplate = true
	o.CacheName = "test-cache"
	o.Timeout = timeconv.Duration(42 * time.Second)
	require.NoError(t, o.Initialize("prom-template"))
	return o
}

func TestInstantiate(t *testing.T) {
	tmpl := newTemplate(t)
	m := discovery.Member{Name: "pod-1", Scheme: "http",
		Address: "10.0.0.1:9090", Weight: 2}
	o, err := Instantiate("my-alb-pod-1", tmpl, m)
	require.NoError(t, err)
	require.Equal(t, "my-alb-pod-1", o.Name)
	require.False(t, o.IsTemplate)
	require.False(t, o.IsDefault)
	require.Equal(t, "http://10.0.0.1:9090", o.OriginURL)
	require.Equal(t, "http", o.Scheme)
	require.Equal(t, "10.0.0.1:9090", o.Host)
	require.Equal(t, "", o.PathPrefix)
	require.Equal(t, "10.0.0.1:9090", o.CacheKeyPrefix)
	// inherited fields
	require.Equal(t, "test-cache", o.CacheName)
	require.Equal(t, timeconv.Duration(42*time.Second), o.Timeout)
	require.Equal(t, "prometheus", o.Provider)
	// template untouched
	require.True(t, tmpl.IsTemplate)
	require.Equal(t, "prom-template", tmpl.Name)
}

func TestInstantiatePathPrefixFallback(t *testing.T) {
	tmpl := newTemplate(t)
	tmpl.OriginURL = "https://ignored.example.com/base"
	require.NoError(t, tmpl.Initialize(tmpl.Name))

	// member without a path inherits the template's path prefix
	o, err := Instantiate("alb-m1", tmpl,
		discovery.Member{Address: "10.0.0.1:9090"})
	require.NoError(t, err)
	require.Equal(t, "http://10.0.0.1:9090/base", o.OriginURL)
	require.Equal(t, "/base", o.PathPrefix)

	// member with a path overrides it
	o, err = Instantiate("alb-m2", tmpl,
		discovery.Member{Address: "10.0.0.1:9090", PathPrefix: "/other"})
	require.NoError(t, err)
	require.Equal(t, "/other", o.PathPrefix)
}

func TestInstantiateErrors(t *testing.T) {
	_, err := Instantiate("x", nil, discovery.Member{Address: "h:1"})
	require.Error(t, err)

	live := bo.New()
	live.Provider = "prometheus"
	require.NoError(t, live.Initialize("live"))
	_, err = Instantiate("x", live, discovery.Member{Address: "h:1"})
	require.Error(t, err, "non-template backends cannot instantiate members")

	tmpl := newTemplate(t)
	_, err = Instantiate("x", tmpl, discovery.Member{})
	require.Error(t, err, "member without address")
}

func TestInstantiateReplicaGroup(t *testing.T) {
	// member-conveyed group overrides everything
	tmpl := bo.New()
	tmpl.Provider = "prometheus"
	tmpl.IsTemplate = true
	tmpl.CacheName = "test-cache"
	require.NoError(t, tmpl.Initialize("prom-template"))
	o, err := Instantiate("alb-m1", tmpl,
		discovery.Member{Address: "10.0.0.1:9090", ReplicaGroup: "shard-a"})
	require.NoError(t, err)
	require.Equal(t, "shard-a", o.ReplicaGroup)

	// an operator-set template group is inherited when the member has none
	tmpl.ReplicaGroup = "one-shard"
	require.NoError(t, tmpl.Initialize(tmpl.Name))
	o, err = Instantiate("alb-m2", tmpl,
		discovery.Member{Address: "10.0.0.2:9090"})
	require.NoError(t, err)
	require.Equal(t, "one-shard", o.ReplicaGroup)

	// ...but a member group still wins over it
	o, err = Instantiate("alb-m3", tmpl,
		discovery.Member{Address: "10.0.0.3:9090", ReplicaGroup: "shard-b"})
	require.NoError(t, err)
	require.Equal(t, "shard-b", o.ReplicaGroup)

	// with neither, each member defaults to its own name (its own shard)
	tmpl.ReplicaGroup = ""
	require.NoError(t, tmpl.Initialize(tmpl.Name))
	o, err = Instantiate("alb-m4", tmpl,
		discovery.Member{Address: "10.0.0.4:9090"})
	require.NoError(t, err)
	require.Equal(t, "alb-m4", o.ReplicaGroup)
}

// TestInstantiateFieldMatrix pins the plan step-6 contract: a discovered
// member overrides exactly name, origin_url (and its derived scheme/host/
// path-prefix), and replica group; every other option -- cache, TLS
// client, healthcheck, paths, rewriters, timeouts, concurrency, tracing --
// inherits from the template.
func TestInstantiateFieldMatrix(t *testing.T) {
	tmpl := bo.New()
	tmpl.Provider = "prometheus"
	tmpl.IsTemplate = true
	tmpl.CacheName = "my-cache"
	tmpl.CacheKeyPrefix = "custom-prefix"
	tmpl.NegativeCacheName = "my-neg"
	tmpl.TracingConfigName = "my-tracer"
	tmpl.ReqRewriterName = "my-rw"
	tmpl.AuthenticatorName = "my-auth"
	tmpl.Timeout = timeconv.Duration(42 * time.Second)
	tmpl.KeepAliveTimeout = timeconv.Duration(21 * time.Second)
	tmpl.MaxIdleConns = 7
	tmpl.MaxConcurrentConns = 9
	tmpl.TimeseriesRetentionFactor = 512
	tmpl.HealthCheck.Path = "/-/healthy"
	tmpl.HealthCheck.Interval = timeconv.Duration(5 * time.Second)
	tmpl.TLS.InsecureSkipVerify = true
	tmpl.Paths = po.List{{Path: "/custom", HandlerName: "proxy"}}
	require.NoError(t, tmpl.Initialize("prom-template"))

	m := discovery.Member{
		Name: "pod-1", Scheme: "https", Address: "10.0.0.1:9090",
		PathPrefix: "/prom", Weight: 3, ReplicaGroup: "shard-0",
	}
	o, err := Instantiate("alb-pod-1", tmpl, m)
	require.NoError(t, err)

	// overridden by the member
	require.Equal(t, "alb-pod-1", o.Name)
	require.Equal(t, "https://10.0.0.1:9090/prom", o.OriginURL)
	require.Equal(t, "https", o.Scheme)
	require.Equal(t, "10.0.0.1:9090", o.Host)
	require.Equal(t, "/prom", o.PathPrefix)
	require.Equal(t, "shard-0", o.ReplicaGroup)
	require.False(t, o.IsTemplate)
	require.False(t, o.IsDefault)

	// inherited from the template
	require.Equal(t, "prometheus", o.Provider)
	require.Equal(t, "my-cache", o.CacheName)
	require.Equal(t, "custom-prefix", o.CacheKeyPrefix,
		"an explicit cache_key_prefix is inherited, not rederived")
	require.Equal(t, "my-neg", o.NegativeCacheName)
	require.Equal(t, "my-tracer", o.TracingConfigName)
	require.Equal(t, "my-rw", o.ReqRewriterName)
	require.Equal(t, "my-auth", o.AuthenticatorName)
	require.Equal(t, timeconv.Duration(42*time.Second), o.Timeout)
	require.Equal(t, timeconv.Duration(21*time.Second), o.KeepAliveTimeout)
	require.Equal(t, 7, o.MaxIdleConns)
	require.Equal(t, 9, o.MaxConcurrentConns)
	require.Equal(t, 512, o.TimeseriesRetentionFactor)
	require.Equal(t, "/-/healthy", o.HealthCheck.Path)
	require.Equal(t, timeconv.Duration(5*time.Second), o.HealthCheck.Interval)
	require.True(t, o.TLS.InsecureSkipVerify)
	require.Len(t, o.Paths, 1)
	require.Equal(t, "/custom", o.Paths[0].Path)

	// deep clone: mutating the instance must not touch the template
	o.HealthCheck.Path = "/mutated"
	o.Paths[0].Path = "/mutated"
	require.Equal(t, "/-/healthy", tmpl.HealthCheck.Path)
	require.Equal(t, "/custom", tmpl.Paths[0].Path)
}
