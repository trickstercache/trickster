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
	"errors"
	"testing"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"
	kubeopts "github.com/trickstercache/trickster/v2/pkg/kube/options"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The discoverer's 'kubernetes' block is the shared connection options; the
// alias is what keeps one vocabulary across discovery and the controller.
func TestOptionsIsTheSharedType(t *testing.T) {
	require.IsType(t, &kubeopts.Options{}, New())
	require.Equal(t, kubeopts.New(), New())
	require.Equal(t, kubeopts.ErrInClusterAndKubeconfig, ErrInClusterAndKubeconfig)
}

// The YAML an operator already has must keep decoding into the alias
func TestExistingDiscoveryYAMLIsUnchanged(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte("kubeconfig: /etc/kube/config\n"), &o))
	require.Equal(t, "/etc/kube/config", o.Kubeconfig)
	require.False(t, o.InCluster)
	require.NoError(t, o.Validate())

	var in Options
	require.NoError(t, yaml.Unmarshal([]byte("in_cluster: true\n"), &in))
	require.True(t, in.InCluster)
	require.NoError(t, in.Validate())

	require.ErrorIs(t,
		(&Options{InCluster: true, Kubeconfig: "/p"}).Validate(),
		ErrInClusterAndKubeconfig)
}

func TestNewErrInvalidOptions(t *testing.T) {
	err := NewErrInvalidOptions("in-cluster",
		"'in_cluster' and 'kubeconfig' are mutually exclusive")
	require.EqualError(t, err,
		`invalid kubernetes options for discoverer "in-cluster": `+
			`'in_cluster' and 'kubeconfig' are mutually exclusive`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target))
}
