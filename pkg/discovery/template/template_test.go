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
