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

package config

import (
	"os"
	"path/filepath"
	"testing"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"

	"github.com/stretchr/testify/require"
)

// The controller must not exist unless the operator asked for it
func TestKubernetesSectionAbsentByDefault(t *testing.T) {
	c := NewConfig()
	require.Nil(t, c.Kubernetes)
	require.False(t, c.Kubernetes.IsEnabled())
	require.Nil(t, c.Clone().Kubernetes)
}

func TestKubernetesSectionLoadsAndInitializes(t *testing.T) {
	c := NewConfig()
	require.NoError(t, c.loadYAMLConfig(`
kubernetes:
  defaults:
    routing_mode: endpoint
`))
	require.NotNil(t, c.Kubernetes)
	require.True(t, c.Kubernetes.IsEnabled())
	require.Equal(t, kubecfg.RoutingModeEndpoint, c.Kubernetes.RoutingMode(),
		"loading must run the section's Initialize")
	require.Equal(t, kubecfg.DefaultGatewayClassControllerName,
		c.Kubernetes.GatewayClassControllerName)
	require.NotNil(t, c.Kubernetes.Connection)
}

// A reload clones the running config; the controller's settings must come
// with it, and must not alias the original
func TestKubernetesSectionClones(t *testing.T) {
	c := NewConfig()
	c.Kubernetes = kubecfg.New()
	c.Kubernetes.Defaults.RoutingMode = kubecfg.RoutingModeService
	c.Kubernetes.WatchNamespaces = []string{"shop"}

	clone := c.Clone()
	require.Equal(t, c.Kubernetes, clone.Kubernetes)
	clone.Kubernetes.WatchNamespaces[0] = "changed"
	clone.Kubernetes.Defaults.RoutingMode = kubecfg.RoutingModeEndpoint
	require.Equal(t, "shop", c.Kubernetes.WatchNamespaces[0])
	require.Equal(t, kubecfg.RoutingModeService, c.Kubernetes.RoutingMode())
}

// An overlay may only define named-object sections, so a controller cannot
// reconfigure the controller
func TestOverlayCannotDefineTheKubernetesSection(t *testing.T) {
	_, ok := overlaySections["kubernetes"]
	require.False(t, ok,
		"an overlay that could rewrite the kubernetes section would let the "+
			"controller redefine its own connection and class claims")
}

// A declared cache that no file backend names is the intended consumer of
// controller-generated backends, so it must survive loading while the
// controller is enabled and be pruned as unreferenced when it is not
func TestKubernetesSectionKeepsUnreferencedCaches(t *testing.T) {
	const base = `
backends:
  placeholder:
    provider: rp
    origin_url: http://127.0.0.1:1
caches:
  objects:
    provider: memory
`
	tests := []struct {
		name      string
		extra     string
		wantCache bool
	}{
		{"controller enabled", "kubernetes:\n  defaults:\n    routing_mode: service\n", true},
		{"controller disabled", "kubernetes:\n  enabled: false\n  defaults:\n    routing_mode: service\n", false},
		{"no kubernetes section", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trickster.yaml")
			require.NoError(t, os.WriteFile(path, []byte(base+test.extra), 0o600))
			c, err := Load([]string{"-config", path})
			require.NoError(t, err)
			_, ok := c.Caches["objects"]
			require.Equal(t, test.wantCache, ok)
		})
	}
}
