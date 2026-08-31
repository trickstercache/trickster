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

package consul

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery"

	"github.com/stretchr/testify/require"
)

func defaultMapping() mapping {
	return mapping{scheme: "http", warningIsReady: true}
}

// Consul's rule: a service that registers its own address overrides the
// node's. Getting this backwards puts traffic on the host instead of the
// sidecar or container that registered itself.
func TestServiceAddressOverridesNodeAddress(t *testing.T) {
	e := entry("web-1", "10.0.0.1", 8080, statusPassing)
	m, err := e.toMember(defaultMapping())
	require.NoError(t, err)
	require.Equal(t, "10.0.0.1:8080", m.Address)

	e.Service.Address = ""
	m, err = e.toMember(defaultMapping())
	require.NoError(t, err)
	require.Equal(t, "10.9.9.9:8080", m.Address, "falls back to the node address")
}

// A member that cannot be addressed is a parse failure, not a silent skip:
// dropping members quietly is how a pool shrinks without anyone noticing.
func TestUnusableEntriesAreErrorsNotSkips(t *testing.T) {
	t.Run("no address anywhere", func(t *testing.T) {
		e := entry("web-1", "", 8080, statusPassing)
		e.Node.Address = ""
		_, err := e.toMember(defaultMapping())
		require.Error(t, err)
	})
	for name, port := range map[string]int{
		"zero port":     0,
		"negative port": -1,
		"port too high": 70000,
	} {
		t.Run(name, func(t *testing.T) {
			e := entry("web-1", "10.0.0.1", port, statusPassing)
			_, err := e.toMember(defaultMapping())
			require.Error(t, err)
		})
	}
	// and the whole snapshot fails rather than yielding a short one
	body := `[{"Node":{"Address":"10.9.9.9"},"Service":{"ID":"a","Port":8080}},
	          {"Node":{"Address":"10.9.9.9"},"Service":{"ID":"b","Port":0}}]`
	_, err := parseCatalog([]byte(body), defaultMapping())
	require.Error(t, err)
	require.Contains(t, err.Error(), "entry 1")
}

// The aggregate is the worst check, which is how Consul itself decides
// whether an instance is serving.
func TestAggregateStatus(t *testing.T) {
	tests := map[string]struct {
		checks []string
		want   string
	}{
		"no checks is passing":   {nil, statusPassing},
		"all passing":            {[]string{statusPassing, statusPassing}, statusPassing},
		"one warning":            {[]string{statusPassing, statusWarning}, statusWarning},
		"critical beats warning": {[]string{statusWarning, statusCritical}, statusCritical},
		"maintenance beats warning": {
			[]string{statusWarning, statusMaintenance}, statusMaintenance,
		},
		"critical beats maintenance": {
			[]string{statusMaintenance, statusCritical}, statusCritical,
		},
		"unknown status wins": {[]string{statusCritical, "some-future-status"}, "some-future-status"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			e := serviceEntry{}
			for i, c := range test.checks {
				e.Checks = append(e.Checks, check{CheckID: string(rune('a' + i)), Status: c})
			}
			require.Equal(t, test.want, e.aggregateStatus())
		})
	}
}

// A status Consul adds in a future release must not be read as healthy.
// Defaulting the other way would silently put unhealthy members in pools.
func TestUnknownStatusIsNotReady(t *testing.T) {
	require.Equal(t, discovery.NotReady, readyFor("some-future-status", true))
	require.Equal(t, discovery.NotReady, readyFor(statusMaintenance, true))
	require.Equal(t, discovery.NotReady, readyFor(statusCritical, true))
	require.Equal(t, discovery.Ready, readyFor(statusPassing, false))
}

func TestWarningIsReadyIsConfigurable(t *testing.T) {
	require.Equal(t, discovery.Ready, readyFor(statusWarning, true))
	require.Equal(t, discovery.NotReady, readyFor(statusWarning, false))

	e := entry("web-1", "10.0.0.1", 8080, statusWarning)
	m, err := e.toMember(mapping{scheme: "http", warningIsReady: false})
	require.NoError(t, err)
	require.Equal(t, discovery.NotReady, m.Ready)
}

// Consul keeps separate passing and warning weights so a degraded instance
// can take less traffic without being removed. That distinction is
// preserved rather than flattened.
func TestConsulWeightsMapToMemberWeight(t *testing.T) {
	e := entry("web-1", "10.0.0.1", 8080, statusPassing)
	e.Service.Weights = &weights{Passing: 10, Warning: 2}
	m, err := e.toMember(defaultMapping())
	require.NoError(t, err)
	require.Equal(t, 10, m.Weight)

	e.Checks[0].Status = statusWarning
	m, err = e.toMember(defaultMapping())
	require.NoError(t, err)
	require.Equal(t, 2, m.Weight, "a warning instance takes its warning weight")

	// absent weights leave the member unweighted, which the pool reads as 1
	e.Service.Weights = nil
	e.Checks[0].Status = statusPassing
	m, err = e.toMember(defaultMapping())
	require.NoError(t, err)
	require.Zero(t, m.Weight)

	// a negative weight is not a negative share of traffic
	e.Service.Weights = &weights{Passing: -5}
	m, err = e.toMember(defaultMapping())
	require.NoError(t, err)
	require.Zero(t, m.Weight)
}

func TestReplicaGroupFromServiceMeta(t *testing.T) {
	e := entry("web-1", "10.0.0.1", 8080, statusPassing)
	e.Service.Meta = map[string]string{"shard": "shard-0"}
	m, err := e.toMember(mapping{scheme: "http", replicaGroupLabel: "shard"})
	require.NoError(t, err)
	require.Equal(t, "shard-0", m.ReplicaGroup)

	// with no key configured, service meta is not treated as a group
	m, err = e.toMember(defaultMapping())
	require.NoError(t, err)
	require.Empty(t, m.ReplicaGroup)
}

// Service metadata is prefixed so an operator-defined key cannot shadow a
// Trickster-assigned label.
func TestLabelsPrefixServiceMeta(t *testing.T) {
	e := entry("web-1", "10.0.0.1", 8080, statusPassing)
	e.Service.Tags = []string{"prod", "v2"}
	e.Service.Meta = map[string]string{"node": "not-the-node", "version": "2"}
	m, err := e.toMember(defaultMapping())
	require.NoError(t, err)
	require.Equal(t, "node-1", m.Labels["node"], "the catalog node must not be shadowed")
	require.Equal(t, "not-the-node", m.Labels["meta_node"])
	require.Equal(t, "2", m.Labels["meta_version"])
	require.Equal(t, ",prod,v2,", m.Labels["tags"],
		"separators bracket the list so a tag match is unambiguous")
	require.Equal(t, statusPassing, m.Labels["status"])
}

func TestNameFallsBackToServiceName(t *testing.T) {
	e := entry("", "10.0.0.1", 8080, statusPassing)
	m, err := e.toMember(defaultMapping())
	require.NoError(t, err)
	require.Equal(t, "web", m.Name)
}

func TestParseCatalogEmptyIsAuthoritative(t *testing.T) {
	for _, empty := range []string{"", "  ", "[]"} {
		snap, err := parseCatalog([]byte(empty), defaultMapping())
		require.NoError(t, err)
		require.Empty(t, snap)
	}
}

func TestParseCatalogRejectsMalformedJSON(t *testing.T) {
	_, err := parseCatalog([]byte("{{{"), defaultMapping())
	require.Error(t, err)
}

func TestSchemeComesFromTheQuery(t *testing.T) {
	e := entry("web-1", "10.0.0.1", 8080, statusPassing)
	m, err := e.toMember(mapping{scheme: "https"})
	require.NoError(t, err)
	require.Equal(t, "https", m.Scheme)
}

func TestReadBodyRejectsOversizedResponse(t *testing.T) {
	_, err := readBody(&infiniteReader{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "limit")
}

type infiniteReader struct{}

func (*infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
