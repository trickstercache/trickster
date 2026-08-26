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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const testLookupYAML = `
in-cluster:
  provider: kubernetes
corp-dns:
  provider: dns_srv
  dns:
    resolver: 10.0.0.53:53
    interval: 45s
`

func TestLookupUnmarshalAndInitialize(t *testing.T) {
	var l Lookup
	require.NoError(t, yaml.Unmarshal([]byte(testLookupYAML), &l))
	require.NoError(t, l.Initialize())
	require.NoError(t, l.Validate())

	k := l["in-cluster"]
	require.NotNil(t, k)
	require.Equal(t, "in-cluster", k.Name)
	require.Equal(t, providers.Kubernetes, k.Provider)
	require.NotNil(t, k.Kubernetes)
	require.True(t, k.Kubernetes.InCluster, "kubernetes defaults to in_cluster")

	d := l["corp-dns"]
	require.NotNil(t, d)
	require.Equal(t, providers.DNSSRV, d.Provider)
	require.NotNil(t, d.DNS)
	require.Equal(t, "10.0.0.53:53", d.DNS.Resolver)
	require.Equal(t, timeconv.Duration(45*time.Second), d.DNS.Interval)
}

func TestInitializeDefaults(t *testing.T) {
	o := &Options{Provider: providers.DNSA}
	require.NoError(t, o.Initialize("d"))
	require.NotNil(t, o.DNS)
	require.Equal(t, timeconv.Duration(DefaultDNSInterval), o.DNS.Interval)

	o = &Options{Provider: providers.Kubernetes,
		Kubernetes: &KubernetesOptions{Kubeconfig: "/tmp/kc"}}
	require.NoError(t, o.Initialize("k"))
	require.False(t, o.Kubernetes.InCluster,
		"a provided kubeconfig must not be overridden to in_cluster")
}

func TestOptionsValidate(t *testing.T) {
	tests := []struct {
		name string
		o    *Options
		ok   bool
	}{
		{"missing provider", &Options{}, false},
		{"invalid provider", &Options{Provider: "consul"}, false},
		{"valid file", &Options{Provider: providers.File}, true},
		{"kube block on file provider", &Options{Provider: providers.File,
			Kubernetes: &KubernetesOptions{}}, false},
		{"dns block on kube provider", &Options{Provider: providers.Kubernetes,
			DNS: &DNSOptions{}}, false},
		{"in_cluster + kubeconfig", &Options{Provider: providers.Kubernetes,
			Kubernetes: &KubernetesOptions{InCluster: true, Kubeconfig: "/x"}}, false},
		{"bad resolver", &Options{Provider: providers.DNSSRV,
			DNS: &DNSOptions{Resolver: "not-host-port"}}, false},
		{"interval too low", &Options{Provider: providers.DNSA,
			DNS: &DNSOptions{Interval: timeconv.Duration(time.Millisecond)}}, false},
		{"valid dns", &Options{Provider: providers.DNSSRV,
			DNS: &DNSOptions{Resolver: "10.0.0.53:53",
				Interval: timeconv.Duration(time.Minute)}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.o.Validate()
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestLookupValidateNilEntry(t *testing.T) {
	l := Lookup{"bad": nil}
	require.Error(t, l.Validate())
}

func TestClone(t *testing.T) {
	o := &Options{Provider: providers.DNSSRV, Name: "d",
		DNS: &DNSOptions{Resolver: "r:53"}}
	c := o.Clone()
	require.Equal(t, o, c)
	c.DNS.Resolver = "other:53"
	require.Equal(t, "r:53", o.DNS.Resolver)

	l := Lookup{"d": o, "nil": nil}
	lc := l.Clone()
	require.Len(t, lc, 1)
	require.Equal(t, o, lc["d"])
}

func TestFileOptions(t *testing.T) {
	// defaults applied for the file provider
	o := &Options{Provider: providers.File}
	require.NoError(t, o.Initialize("f"))
	require.NotNil(t, o.File)
	require.Equal(t, timeconv.Duration(DefaultFilePollInterval),
		o.File.PollInterval)
	_, err := o.Validate()
	require.NoError(t, err)

	// explicit interval preserved
	o = &Options{Provider: providers.File,
		File: &FileOptions{PollInterval: timeconv.Duration(5 * time.Second)}}
	require.NoError(t, o.Initialize("f"))
	require.Equal(t, timeconv.Duration(5*time.Second), o.File.PollInterval)

	// file block on a non-file provider is rejected
	o = &Options{Provider: providers.Kubernetes, File: &FileOptions{}}
	_, err = o.Validate()
	require.Error(t, err)

	// sub-minimum poll interval is rejected
	o = &Options{Provider: providers.File,
		File: &FileOptions{PollInterval: timeconv.Duration(time.Millisecond)}}
	_, err = o.Validate()
	require.Error(t, err)

	// clone is deep
	o = &Options{Provider: providers.File,
		File: &FileOptions{PollInterval: timeconv.Duration(time.Minute)}}
	c := o.Clone()
	c.File.PollInterval = 0
	require.Equal(t, timeconv.Duration(time.Minute), o.File.PollInterval)
}
