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

package memberlist

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery"

	"github.com/stretchr/testify/require"
)

// A malformed member list must be rejected outright rather than partially
// applied: the caller keeps its last-good membership, and a half-parsed
// list would silently drain a pool.
func TestParseEntryValidation(t *testing.T) {
	for _, bad := range []string{
		"- name: no-address\n",
		"- address: not-host-port\n",
		"- address: 10.0.0.1:9090\n  scheme: gopher\n",
		"- address: 10.0.0.1:9090\n  weight: -1\n",
		"not a list\n",
	} {
		_, err := Parse([]byte(bad))
		require.Error(t, err, "expected error for %q", bad)
	}
	snap, err := Parse([]byte("- address: 10.0.0.1:9090\n"))
	require.NoError(t, err)
	require.Len(t, snap, 1)
}

func TestParseDefaults(t *testing.T) {
	snap, err := Parse([]byte("- address: 10.0.0.1:9090\n"))
	require.NoError(t, err)
	require.Equal(t, "10.0.0.1:9090", snap[0].Name, "name defaults to the address")
	require.Equal(t, SchemeHTTP, snap[0].Scheme)
	require.Equal(t, discovery.ReadyUnknown, snap[0].Ready)
}

// JSON is a subset of YAML, so the native format accepts either.
func TestParseJSON(t *testing.T) {
	snap, err := Parse(
		[]byte(`[{"name": "j1", "address": "10.0.0.1:9090", "weight": 2}]`))
	require.NoError(t, err)
	require.Len(t, snap, 1)
	require.Equal(t, "j1", snap[0].Name)
	require.Equal(t, 2, snap[0].Weight)
}

func TestParseReplicaGroup(t *testing.T) {
	snap, err := Parse([]byte(`
- name: prom-a
  address: 10.0.0.1:9090
  replica_group: shard-0
- name: prom-b
  address: 10.0.0.2:9090
`))
	require.NoError(t, err)
	require.Len(t, snap, 2)
	require.Equal(t, "shard-0", snap[0].ReplicaGroup)
	require.Empty(t, snap[1].ReplicaGroup)
}

// An empty document is an authoritative empty membership, not a failure --
// a source that genuinely has no members must be able to say so.
func TestParseEmptyIsAuthoritative(t *testing.T) {
	for _, empty := range []string{"", "   \n", "[]"} {
		snap, err := Parse([]byte(empty))
		require.NoError(t, err)
		require.Empty(t, snap)
	}
}

func TestParsePrometheus(t *testing.T) {
	snap, err := ParsePrometheus([]byte(`[
	  {"targets": ["10.0.0.1:9090", "10.0.0.2:9090"], "labels": {"env": "prod"}},
	  {"targets": ["10.0.0.3:9090"]}
	]`), SchemeHTTP)
	require.NoError(t, err)
	require.Len(t, snap, 3)
	require.Equal(t, "10.0.0.1:9090", snap[0].Address)
	require.Equal(t, "10.0.0.1:9090", snap[0].Name)
	require.Equal(t, SchemeHTTP, snap[0].Scheme)
	require.Equal(t, "prod", snap[0].Labels["env"], "group labels carry onto each member")
	require.Nil(t, snap[2].Labels, "an unlabeled group yields no label map")
}

func TestParsePrometheusDefaultScheme(t *testing.T) {
	snap, err := ParsePrometheus([]byte(`[{"targets": ["10.0.0.1:9090"]}]`), SchemeHTTPS)
	require.NoError(t, err)
	require.Equal(t, SchemeHTTPS, snap[0].Scheme)

	// an unset default is http, matching the native format's default
	snap, err = ParsePrometheus([]byte(`[{"targets": ["10.0.0.1:9090"]}]`), "")
	require.NoError(t, err)
	require.Equal(t, SchemeHTTP, snap[0].Scheme)
}

// __scheme__ lets one endpoint serve mixed-scheme members, as it does for
// Prometheus itself; it is consumed as the scheme rather than left as a
// member label.
func TestParsePrometheusSchemeLabel(t *testing.T) {
	snap, err := ParsePrometheus([]byte(`[
	  {"targets": ["10.0.0.1:9090"], "labels": {"__scheme__": "https", "env": "prod"}},
	  {"targets": ["10.0.0.2:9090"]}
	]`), SchemeHTTP)
	require.NoError(t, err)
	require.Equal(t, SchemeHTTPS, snap[0].Scheme, "the label overrides the default")
	require.NotContains(t, snap[0].Labels, PrometheusSchemeLabel,
		"the consumed meta-label should not also appear as member metadata")
	require.Equal(t, "prod", snap[0].Labels["env"])
	require.Equal(t, SchemeHTTP, snap[1].Scheme, "other groups keep the default")

	// a group whose only label was __scheme__ ends up with no labels at all
	snap, err = ParsePrometheus(
		[]byte(`[{"targets": ["10.0.0.1:9090"], "labels": {"__scheme__": "https"}}]`),
		SchemeHTTP)
	require.NoError(t, err)
	require.Nil(t, snap[0].Labels)
}

func TestParsePrometheusValidation(t *testing.T) {
	for name, bad := range map[string]string{
		"not json":             `- targets: [10.0.0.1:9090]`,
		"not an array":         `{"targets": ["10.0.0.1:9090"]}`,
		"target not host:port": `[{"targets": ["10.0.0.1"]}]`,
		"empty target":         `[{"targets": [""]}]`,
		"bad scheme label":     `[{"targets": ["10.0.0.1:9090"], "labels": {"__scheme__": "gopher"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePrometheus([]byte(bad), SchemeHTTP)
			require.Error(t, err)
		})
	}
}

func TestParsePrometheusEmptyIsAuthoritative(t *testing.T) {
	for _, empty := range []string{"", "  ", "[]"} {
		snap, err := ParsePrometheus([]byte(empty), SchemeHTTP)
		require.NoError(t, err)
		require.Empty(t, snap)
	}
}

// The Prometheus format cannot express weight or replica group; that is a
// documented reason to prefer the native format, and is pinned here so the
// limitation is not quietly "fixed" by inventing a label convention.
func TestParsePrometheusCarriesNoWeightOrReplicaGroup(t *testing.T) {
	snap, err := ParsePrometheus(
		[]byte(`[{"targets": ["10.0.0.1:9090"], "labels": {"weight": "5", "replica_group": "shard-0"}}]`),
		SchemeHTTP)
	require.NoError(t, err)
	require.Zero(t, snap[0].Weight)
	require.Empty(t, snap[0].ReplicaGroup)
	require.Equal(t, "5", snap[0].Labels["weight"],
		"they remain ordinary labels, not pool semantics")
}
