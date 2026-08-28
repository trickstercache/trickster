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

	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"

	"github.com/stretchr/testify/require"
)

func TestQueryValidate(t *testing.T) {
	tests := []struct {
		name     string
		q        *Query
		provider string
		ok       bool
	}{
		{"bad scheme", &Query{Scheme: "gopher", Path: "/x"},
			providers.File, false},
		{"unknown provider", &Query{}, "consul", false},

		// kubernetes
		{"kube default kind requires service",
			&Query{Namespace: "monitoring"}, providers.Kubernetes, false},
		{"kube endpointslices ok",
			&Query{Service: "prom", Namespace: "monitoring", Port: "web"},
			providers.Kubernetes, true},
		{"kube pods requires selector", &Query{Kind: KindPods},
			providers.Kubernetes, false},
		{"kube pods with service invalid",
			&Query{Kind: KindPods, Service: "x",
				Selector: map[string]string{"app": "prom"}},
			providers.Kubernetes, false},
		{"kube pods ok",
			&Query{Kind: KindPods, Selector: map[string]string{"app": "prom"}},
			providers.Kubernetes, true},
		{"kube service by selector ok",
			&Query{Kind: KindService, Selector: map[string]string{"app": "prom"}},
			providers.Kubernetes, true},
		{"kube bad kind", &Query{Kind: "deployments", Service: "x"},
			providers.Kubernetes, false},
		{"kube bad port", &Query{Service: "prom", Port: "Not_A_Port_Name!"},
			providers.Kubernetes, false},
		{"kube srv_name invalid", &Query{Service: "prom", SRVName: "x"},
			providers.Kubernetes, false},

		// dns_srv
		{"srv requires srv_name", &Query{}, providers.DNSSRV, false},
		{"srv ok", &Query{SRVName: "_prom._tcp.example.com"},
			providers.DNSSRV, true},
		{"srv port invalid", &Query{SRVName: "x", Port: "80"},
			providers.DNSSRV, false},
		{"srv selector invalid",
			&Query{SRVName: "x", Selector: map[string]string{"a": "b"}},
			providers.DNSSRV, false},

		// dns_a
		{"dns_a requires hostname", &Query{Port: "9090"}, providers.DNSA, false},
		{"dns_a requires port", &Query{Hostname: "prom.internal"},
			providers.DNSA, false},
		{"dns_a bad port", &Query{Hostname: "prom.internal", Port: "web"},
			providers.DNSA, false},
		{"dns_a ok", &Query{Hostname: "prom.internal", Port: "9090",
			Scheme: "https"}, providers.DNSA, true},

		// file
		{"file requires path", &Query{}, providers.File, false},
		{"file ok", &Query{Path: "/etc/trickster/members.yaml"},
			providers.File, true},
		{"file scheme invalid", &Query{Path: "/x", Scheme: "http"},
			providers.File, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.q.Validate("test-alb", tc.provider)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestQueryValidateDefaultsKind(t *testing.T) {
	q := &Query{Service: "prom"}
	require.NoError(t, q.Validate("alb", providers.Kubernetes))
	require.Equal(t, KindEndpointSlices, q.Kind)
}

func TestQueryClone(t *testing.T) {
	q := &Query{Kind: KindPods, Selector: map[string]string{"app": "prom"}}
	c := q.Clone()
	require.Equal(t, q, c)
	c.Selector["app"] = "other"
	require.Equal(t, "prom", q.Selector["app"])
}

func TestQueryReplicaGroupLabelValidation(t *testing.T) {
	// kubernetes accepts it on every kind
	q := &Query{Service: "prom", ReplicaGroupLabel: "prometheus/replica"}
	require.NoError(t, q.Validate("alb", providers.Kubernetes))
	q = &Query{Kind: KindPods, Selector: map[string]string{"a": "b"},
		ReplicaGroupLabel: "prometheus/replica"}
	require.NoError(t, q.Validate("alb", providers.Kubernetes))

	// non-kubernetes providers reject it
	q = &Query{SRVName: "x", ReplicaGroupLabel: "l"}
	require.Error(t, q.Validate("alb", providers.DNSSRV))
	q = &Query{Hostname: "h", Port: "80", ReplicaGroupLabel: "l"}
	require.Error(t, q.Validate("alb", providers.DNSA))
	q = &Query{Path: "/x", ReplicaGroupLabel: "l"}
	require.Error(t, q.Validate("alb", providers.File))
}
