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

package nomad

import (
	"encoding/json"
	"testing"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/discovery"

	"github.com/stretchr/testify/require"
)

func defaultMapping() mapping { return mapping{scheme: "http"} }

func jsonOf(v any) ([]byte, error) { return json.Marshal(v) }

// A member that cannot be addressed is a parse failure, not a silent skip:
// dropping members quietly is how a pool shrinks without anyone noticing.
func TestUnusableRegistrationsAreErrorsNotSkips(t *testing.T) {
	t.Run("no address", func(t *testing.T) {
		r := reg("web-1", "", 8080)
		_, err := r.toMember(defaultMapping())
		require.Error(t, err)
	})
	for name, port := range map[string]int{
		"zero port":     0,
		"negative port": -1,
		"port too high": 70000,
	} {
		t.Run(name, func(t *testing.T) {
			r := reg("web-1", "10.0.0.1", port)
			_, err := r.toMember(defaultMapping())
			require.Error(t, err)
		})
	}
	body := `[{"ID":"a","Address":"10.0.0.1","Port":8080},
	          {"ID":"b","Address":"10.0.0.2","Port":0}]`
	_, err := parseRegistry([]byte(body), defaultMapping())
	require.Error(t, err)
	require.Contains(t, err.Error(), "registration 1")
}

// Nomad's service endpoint has no tag parameter, unlike Consul's, so the
// tag filter is applied here. It is a conjunction: every listed tag must be
// present.
func TestTagFilterIsClientSideAndConjunctive(t *testing.T) {
	regs := []serviceRegistration{
		reg("web-1", "10.0.0.1", 8080, "prod", "v2"),
		reg("web-2", "10.0.0.2", 8080, "prod"),
		reg("web-3", "10.0.0.3", 8080),
	}
	body, err := jsonOf(regs)
	require.NoError(t, err)

	snap, err := parseRegistry(body, mapping{scheme: "http"})
	require.NoError(t, err)
	require.Len(t, snap, 3, "no tags means no filtering")

	snap, err = parseRegistry(body, mapping{scheme: "http", tags: []string{"prod"}})
	require.NoError(t, err)
	require.Len(t, snap, 2)

	snap, err = parseRegistry(body, mapping{scheme: "http", tags: []string{"prod", "v2"}})
	require.NoError(t, err)
	require.Len(t, snap, 1, "every listed tag must be present, not any")
	require.Equal(t, "10.0.0.1:8080", snap[0].Address)

	snap, err = parseRegistry(body, mapping{scheme: "http", tags: []string{"absent"}})
	require.NoError(t, err)
	require.Empty(t, snap)
}

// A filtered-out registration must not be validated: a malformed member the
// operator has excluded should not fail the whole refresh.
func TestFilteredOutRegistrationsAreNotValidated(t *testing.T) {
	body := `[{"ID":"a","Address":"10.0.0.1","Port":8080,"Tags":["keep"]},
	          {"ID":"b","Address":"10.0.0.2","Port":0,"Tags":["drop"]}]`
	snap, err := parseRegistry([]byte(body), mapping{scheme: "http", tags: []string{"keep"}})
	require.NoError(t, err)
	require.Len(t, snap, 1)
}

func TestLabels(t *testing.T) {
	r := reg("web-1", "10.0.0.1", 8080, "prod", "v2")
	m, err := r.toMember(defaultMapping())
	require.NoError(t, err)
	require.Equal(t, "web", m.Labels["service"])
	require.Equal(t, "web-1", m.Labels["service_id"])
	require.Equal(t, "web-job", m.Labels["job_id"])
	require.Equal(t, "alloc-1", m.Labels["alloc_id"])
	require.Equal(t, "node-1", m.Labels["node_id"])
	require.Equal(t, "default", m.Labels["namespace"])
	require.Equal(t, "dc1", m.Labels["datacenter"])
	require.Equal(t, ",prod,v2,", m.Labels["tags"],
		"separators bracket the list so a tag match is unambiguous")

	// empty fields are dropped rather than carried as blank labels
	bare := serviceRegistration{ID: "x", Address: "10.0.0.1", Port: 1}
	m, err = bare.toMember(defaultMapping())
	require.NoError(t, err)
	require.NotContains(t, m.Labels, "job_id")
}

func TestNameFallsBackToServiceName(t *testing.T) {
	r := reg("", "10.0.0.1", 8080)
	m, err := r.toMember(defaultMapping())
	require.NoError(t, err)
	require.Equal(t, "web", m.Name)
}

func TestSchemeComesFromTheQuery(t *testing.T) {
	r := reg("web-1", "10.0.0.1", 8080)
	m, err := r.toMember(mapping{scheme: "https"})
	require.NoError(t, err)
	require.Equal(t, "https", m.Scheme)
	require.Equal(t, discovery.ReadyUnknown, m.Ready)
}

func TestParseRegistryEmptyIsAuthoritative(t *testing.T) {
	for _, empty := range []string{"", "  ", "[]"} {
		snap, err := parseRegistry([]byte(empty), defaultMapping())
		require.NoError(t, err)
		require.Empty(t, snap)
	}
}

func TestParseRegistryRejectsMalformedJSON(t *testing.T) {
	_, err := parseRegistry([]byte("{{{"), defaultMapping())
	require.Error(t, err)
}

// The bounded read itself is tested in pkg/bytes; this pins that this
// provider actually applies a limit, so a refactor cannot quietly remove it.
func TestResponseSizeIsBounded(t *testing.T) {
	_, err := tbytes.ReadBoundedBody(&infiniteReader{}, maxResponseBytes, false)
	require.ErrorIs(t, err, tbytes.ErrBodyTooLarge)
}

type infiniteReader struct{}

func (*infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
