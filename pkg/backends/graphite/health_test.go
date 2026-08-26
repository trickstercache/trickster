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

package graphite

import (
	"maps"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

func TestDefaultHealthCheckConfig(t *testing.T) {
	c, _ := NewClient("test", bo.New(), nil, nil, nil, nil)

	dho := c.DefaultHealthCheckConfig()
	require.NotNil(t, dho)

	if !strings.HasSuffix(dho.Path, healthPath) {
		t.Errorf("expected path to end with %s, got %s", healthPath, dho.Path)
	}
	if dho.Query != healthQuery {
		t.Errorf("expected query %s got %s", healthQuery, dho.Query)
	}
}

func TestFinalizeHealthCheckOptions(t *testing.T) {
	newAuthed := func(t *testing.T) *Client {
		o := bo.New()
		o.Graphite = gro.New()
		o.Graphite.OriginAuthorization = "Bearer tok"
		c, err := NewClient("test", o, nil, nil, nil, nil)
		require.NoError(t, err)
		return c.(*Client)
	}
	overlaid := func(c *Client, custom *ho.Options) *ho.Options {
		o := c.DefaultHealthCheckConfig()
		o.Overlay(custom)
		return c.FinalizeHealthCheckOptions(o)
	}

	c := newAuthed(t)

	// no custom options: the default credential remains
	o := overlaid(c, nil)
	require.Equal(t, "Bearer tok", o.Headers["Authorization"])
	require.Len(t, o.Headers, 1)

	// a custom non-auth header must not drop the credential
	o = overlaid(c, &ho.Options{Headers: map[string]string{"X-Probe": "trickster"}})
	require.Equal(t, "trickster", o.Headers["X-Probe"])
	require.Equal(t, "Bearer tok", o.Headers["Authorization"])
	require.Len(t, o.Headers, 2)

	// a custom Authorization overrides the credential, any casing
	o = overlaid(c, &ho.Options{Headers: map[string]string{"Authorization": "Basic other"}})
	require.Equal(t, map[string]string{"Authorization": "Basic other"}, map[string]string(o.Headers))
	o = overlaid(c, &ho.Options{Headers: map[string]string{"authorization": "Basic other"}})
	require.Equal(t, map[string]string{"authorization": "Basic other"}, map[string]string(o.Headers))

	// an empty custom Authorization opts the probe out: the effective clone
	// omits the header, the stored marker survives, and re-finalizing holds
	stored := c.DefaultHealthCheckConfig()
	stored.Overlay(&ho.Options{Headers: map[string]string{"Authorization": ""}})
	effective := c.FinalizeHealthCheckOptions(stored)
	require.NotContains(t, effective.Headers, "Authorization")
	require.Equal(t, map[string]string{"Authorization": ""}, map[string]string(stored.Headers))
	effective = c.FinalizeHealthCheckOptions(stored)
	require.NotContains(t, effective.Headers, "Authorization")
	require.Equal(t, map[string]string{"Authorization": ""}, map[string]string(stored.Headers))

	// a non-Authorization empty header is not the opt-out and rides as-is
	o = overlaid(c, &ho.Options{Headers: map[string]string{"X-Probe-Flag": ""}})
	require.Equal(t, "", o.Headers["X-Probe-Flag"])
	require.Contains(t, o.Headers, "X-Probe-Flag")
	require.Equal(t, "Bearer tok", o.Headers["Authorization"])

	// without a configured credential the finalizer changes nothing
	plain, err := NewClient("test", bo.New(), nil, nil, nil, nil)
	require.NoError(t, err)
	o = plain.(*Client).DefaultHealthCheckConfig()
	o.Overlay(&ho.Options{Headers: map[string]string{"X-Probe": "trickster"}})
	o = plain.(*Client).FinalizeHealthCheckOptions(o)
	require.Equal(t, map[string]string{"X-Probe": "trickster"}, map[string]string(o.Headers))

	// a nil options value must not panic
	require.Nil(t, c.FinalizeHealthCheckOptions(nil))
}

func TestFinalizeHealthCheckOptionsCasingCollisions(t *testing.T) {
	newClient := func(t *testing.T, originAuth string) *Client {
		o := bo.New()
		o.Graphite = gro.New()
		o.Graphite.OriginAuthorization = originAuth
		b, err := NewClient("test", o, nil, nil, nil, nil)
		require.NoError(t, err)
		return b.(*Client)
	}

	// the winner must be deterministic over repeated probe construction: the
	// canonical key first, then the lexicographically first colliding key
	tests := []struct {
		name       string
		originAuth string
		headers    map[string]string
		expected   []string
	}{
		{"canonical empty beats lowercase credential", "Bearer tok",
			map[string]string{"Authorization": "", "authorization": "Basic alternate"},
			nil},
		{"canonical credential beats lowercase empty", "Bearer tok",
			map[string]string{"Authorization": "Basic primary", "authorization": ""},
			[]string{"Basic primary"}},
		{"two non-canonical credentials pick the first sorted key", "Bearer tok",
			map[string]string{"AUTHORIZATION": "Basic upper", "authorization": "Basic lower"},
			[]string{"Basic upper"}},
		{"collisions collapse without an origin credential too", "",
			map[string]string{"Authorization": "Basic primary", "authorization": "Basic alternate"},
			[]string{"Basic primary"}},
		{"empty winner without a credential stays a present empty header", "",
			map[string]string{"AUTHORIZATION": "", "authorization": "Basic lower"},
			[]string{""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, tc.originAuth)
			for range 20 {
				stored := c.DefaultHealthCheckConfig()
				stored.Overlay(&ho.Options{Headers: maps.Clone(tc.headers)})
				effective := c.FinalizeHealthCheckOptions(stored)
				h := headers.Lookup(effective.Headers).ToHeader()
				require.Equal(t, tc.expected, h.Values("Authorization"))
			}
		})
	}
}

func TestStartHealthChecksAppliesOriginAuth(t *testing.T) {
	o := bo.New()
	o.Graphite = gro.New()
	o.Graphite.OriginAuthorization = "Bearer tok"
	c := newTestClient(t, o)
	o.HealthCheck = &ho.Options{Headers: map[string]string{"X-Probe": "trickster"}}

	hc, err := backends.Backends{"test": c}.StartHealthChecks(nil)
	require.NoError(t, err)
	defer hc.Shutdown()

	require.Equal(t, map[string]string{
		"X-Probe":       "trickster",
		"Authorization": "Bearer tok",
	}, map[string]string(o.HealthCheck.Headers))
}

func TestStartHealthChecksAuthOptOutIdempotent(t *testing.T) {
	o := bo.New()
	o.Graphite = gro.New()
	o.Graphite.OriginAuthorization = "Bearer tok"
	c := newTestClient(t, o)
	o.HealthCheck = &ho.Options{Headers: map[string]string{"Authorization": ""}}

	// two consecutive setups over the same options object must both yield an
	// unauthenticated probe, and the opt-out must survive for config export
	for i := range 2 {
		hc, err := backends.Backends{"test": c}.StartHealthChecks(nil)
		require.NoError(t, err, "pass %d", i)
		hc.Shutdown()
		v, ok := o.HealthCheck.Headers["Authorization"]
		require.True(t, ok, "pass %d must retain the opt-out marker", i)
		require.Empty(t, v, "pass %d must not restore the credential", i)
	}
}
