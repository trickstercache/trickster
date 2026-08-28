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

	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func newTestDiscoveryOptions() *DiscoveryOptions {
	return &DiscoveryOptions{
		DiscovererName:  "in-cluster",
		TemplateBackend: "prom-template",
		Query:           &do.Query{Service: "prom"},
	}
}

func TestDiscoveryOptionsInitialize(t *testing.T) {
	d := newTestDiscoveryOptions()
	require.NoError(t, d.Initialize(""))
	require.Equal(t, StartupPolicyRetry, d.StartupPolicy)
}

func TestDiscoveryOptionsValidate(t *testing.T) {
	d := newTestDiscoveryOptions()
	_, err := d.Validate()
	require.NoError(t, err)

	d = newTestDiscoveryOptions()
	d.DiscovererName = ""
	_, err = d.Validate()
	require.ErrorIs(t, err, ErrDiscovererNameRequired)

	d = newTestDiscoveryOptions()
	d.TemplateBackend = ""
	_, err = d.Validate()
	require.ErrorIs(t, err, ErrTemplateBackendRequired)

	d = newTestDiscoveryOptions()
	d.Query = nil
	_, err = d.Validate()
	require.ErrorIs(t, err, ErrDiscoveryQueryRequired)

	d = newTestDiscoveryOptions()
	d.MinMembers = -1
	_, err = d.Validate()
	require.ErrorIs(t, err, ErrInvalidMinMembers)

	d = newTestDiscoveryOptions()
	d.DebounceWindow = -1
	_, err = d.Validate()
	require.ErrorIs(t, err, ErrInvalidDebounceWindow)

	d = newTestDiscoveryOptions()
	d.StartupPolicy = "explode"
	_, err = d.Validate()
	require.ErrorIs(t, err, ErrInvalidStartupPolicy)

	d = newTestDiscoveryOptions()
	d.StartupPolicy = StartupPolicyFail
	_, err = d.Validate()
	require.NoError(t, err)
}

func TestDiscoveryOptionsClone(t *testing.T) {
	d := newTestDiscoveryOptions()
	d.MinMembers = 2
	c := d.Clone()
	require.Equal(t, d, c)
	c.Query.Service = "other"
	require.Equal(t, "prom", d.Query.Service)
}

func TestALBOptionsWithDiscoveryYAML(t *testing.T) {
	var o Options
	err := yaml.Unmarshal([]byte(`
mechanism: rr
pool: [static-member]
discovery:
  discoverer_name: in-cluster
  template_backend: prom-template
  query:
    kind: endpointslices
    namespace: monitoring
    service: prom
  min_members: 1
  debounce_window: 5s
  startup_policy: fail
`), &o)
	require.NoError(t, err)
	require.NoError(t, o.Initialize(""))
	require.NotNil(t, o.Discovery)
	require.Equal(t, "in-cluster", o.Discovery.DiscovererName)
	require.Equal(t, "prom-template", o.Discovery.TemplateBackend)
	require.Equal(t, StartupPolicyFail, o.Discovery.StartupPolicy)
	require.Equal(t, 1, o.Discovery.MinMembers)
	require.NotNil(t, o.Discovery.Query)
	require.Equal(t, "monitoring", o.Discovery.Query.Namespace)
	_, err = o.Validate()
	require.NoError(t, err)

	c := o.Clone()
	require.Equal(t, o.Discovery, c.Discovery)

	// mixed pools are permitted: static members remain alongside discovery
	require.Equal(t, Members("static-member"), o.Pool)
}
