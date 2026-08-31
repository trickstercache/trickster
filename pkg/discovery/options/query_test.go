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
			err := tc.q.Validate("test-alb", &Options{Provider: tc.provider})
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
	require.NoError(t, q.Validate("alb", &Options{Provider: providers.Kubernetes}))
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
	require.NoError(t, q.Validate("alb", &Options{Provider: providers.Kubernetes}))
	q = &Query{Kind: KindPods, Selector: map[string]string{"a": "b"},
		ReplicaGroupLabel: "prometheus/replica"}
	require.NoError(t, q.Validate("alb", &Options{Provider: providers.Kubernetes}))

	// non-kubernetes providers reject it
	q = &Query{SRVName: "x", ReplicaGroupLabel: "l"}
	require.Error(t, q.Validate("alb", &Options{Provider: providers.DNSSRV}))
	q = &Query{Hostname: "h", Port: "80", ReplicaGroupLabel: "l"}
	require.Error(t, q.Validate("alb", &Options{Provider: providers.DNSA}))
	q = &Query{Path: "/x", ReplicaGroupLabel: "l"}
	require.Error(t, q.Validate("alb", &Options{Provider: providers.File}))
}

// Query.Validate takes the whole discoverer Options because field
// applicability is not always a function of the provider name alone -- the
// forthcoming 'aws' provider varies by aws.service. These pin the
// contract's edges.

func TestQueryValidateRequiresOptions(t *testing.T) {
	q := &Query{Path: "/tmp/members.yaml"}
	err := q.Validate("alb", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no discoverer options")
}

func TestQueryValidateUnknownProvider(t *testing.T) {
	q := &Query{Path: "/tmp/members.yaml"}
	err := q.Validate("alb", &Options{Provider: "not_a_provider"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown discovery provider")
}

// Every provider must declare its accepted query fields. A provider added
// to the supported set without a table entry would otherwise fail every
// query with "unknown discovery provider" at startup, which points at the
// operator's config rather than at the missing registration.
func TestEveryProviderDeclaresAcceptedQueryFields(t *testing.T) {
	for p := range providers.SupportedProviders() {
		if _, ok := providerQueryFields[p]; !ok {
			t.Errorf("provider %q has no entry in providerQueryFields", p)
		}
	}
}

// Every field named in a provider's accepted set must exist in queryFields,
// or it can never be read and the acceptance is a no-op.
func TestAcceptedQueryFieldsAreKnownFields(t *testing.T) {
	known := make(map[string]bool, len(queryFields))
	for _, f := range queryFields {
		known[f.name] = true
	}
	for p, accepted := range providerQueryFields {
		for name := range accepted {
			if !known[name] {
				t.Errorf("provider %q accepts unknown query field %q", p, name)
			}
		}
	}
}

// The table is the rejection mechanism: a field set for a provider that
// does not accept it must fail startup rather than be silently ignored.
// This walks every field against every provider, which is the check the
// previous per-provider requireUnset lists could drift away from.
func TestEveryUnacceptedFieldIsRejected(t *testing.T) {
	setters := map[string]func(*Query){
		"kind":                func(q *Query) { q.Kind = KindPods },
		"namespace":           func(q *Query) { q.Namespace = "ns" },
		"service":             func(q *Query) { q.Service = "svc" },
		"selector":            func(q *Query) { q.Selector = map[string]string{"a": "b"} },
		"port":                func(q *Query) { q.Port = "9090" },
		"replica_group_label": func(q *Query) { q.ReplicaGroupLabel = "shard" },
		"srv_name":            func(q *Query) { q.SRVName = "_x._tcp.example.com" },
		"hostname":            func(q *Query) { q.Hostname = "example.com" },
		"path":                func(q *Query) { q.Path = "/tmp/members.yaml" },
		"scheme":              func(q *Query) { q.Scheme = SchemeHTTPS },
		"filter":              func(q *Query) { q.Filter = `Service.Meta.v == "2"` },
		"tags":                func(q *Query) { q.Tags = []string{"prod"} },
	}
	// every field in the table must have a setter here, or the sweep below
	// silently skips it
	for _, f := range queryFields {
		if _, ok := setters[f.name]; !ok {
			t.Fatalf("query field %q has no setter in this test", f.name)
		}
	}
	for p, accepted := range providerQueryFields {
		for name, set := range setters {
			if accepted.Contains(name) {
				continue
			}
			t.Run(p+"/"+name, func(t *testing.T) {
				q := &Query{}
				set(q)
				err := q.Validate("alb", &Options{Provider: p})
				require.Error(t, err, "%q should not be valid for %s", name, p)
				require.Contains(t, err.Error(), name)
			})
		}
	}
}

func TestQueryValidateHTTPSD(t *testing.T) {
	o := &Options{Provider: providers.HTTPSD}
	// path is optional: the endpoint alone is a complete target
	require.NoError(t, (&Query{}).Validate("alb", o))
	require.NoError(t, (&Query{Path: "/pools/a"}).Validate("alb", o))
	require.NoError(t, (&Query{Scheme: SchemeHTTPS}).Validate("alb", o))

	// a path that is not rooted would silently resolve against the
	// endpoint's directory rather than its root
	err := (&Query{Path: "pools/a"}).Validate("alb", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must begin with '/'")
}

func TestQueryValidateConsul(t *testing.T) {
	o := &Options{Provider: providers.Consul}
	require.NoError(t, (&Query{Service: "web"}).Validate("alb", o))
	require.NoError(t, (&Query{
		Service: "web", Tags: []string{"prod"}, Filter: `Service.Meta.v == "2"`,
		Scheme: SchemeHTTPS, ReplicaGroupLabel: "shard",
	}).Validate("alb", o))

	err := (&Query{}).Validate("alb", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "'service' is required")

	err = (&Query{Service: "web", Tags: []string{"prod", ""}}).Validate("alb", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be empty")
}

// Tags is the first slice field on Query; Clone must copy it or two ALBs
// sharing a template would share the backing array.
func TestQueryCloneCopiesTags(t *testing.T) {
	q := &Query{Service: "web", Tags: []string{"prod"}}
	c := q.Clone()
	c.Tags[0] = "staging"
	require.Equal(t, "prod", q.Tags[0])
}

func TestQueryValidateNomad(t *testing.T) {
	o := &Options{Provider: providers.Nomad}
	require.NoError(t, (&Query{Service: "web"}).Validate("alb", o))
	require.NoError(t, (&Query{
		Service: "web", Tags: []string{"prod"}, Filter: `JobID == "web"`,
		Scheme: SchemeHTTPS,
	}).Validate("alb", o))

	err := (&Query{}).Validate("alb", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "'service' is required")

	err = (&Query{Service: "web", Tags: []string{""}}).Validate("alb", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be empty")

	// nomad's native registry carries no health, so there is no replica
	// group metadata to read either
	err = (&Query{Service: "web", ReplicaGroupLabel: "shard"}).Validate("alb", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "replica_group_label")
}
