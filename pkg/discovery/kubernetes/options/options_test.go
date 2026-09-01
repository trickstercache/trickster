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

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// In-cluster is the idiomatic deployment, so it is what an otherwise empty
// block means.
func TestNewDefaultsToInCluster(t *testing.T) {
	require.True(t, New().InCluster)
	require.Empty(t, New().Kubeconfig)
}

func TestInitializeDefaultsToInClusterOnlyWithoutAKubeconfig(t *testing.T) {
	o := &Options{}
	o.Initialize()
	require.True(t, o.InCluster, "an empty block means the pod's service account")

	// a kubeconfig is an explicit choice to run outside the cluster, so
	// Initialize must not override it into an invalid pairing
	ext := &Options{Kubeconfig: "/path/to/kubeconfig"}
	ext.Initialize()
	require.False(t, ext.InCluster)
	require.NoError(t, ext.Validate(),
		"Initialize must not create the combination Validate rejects")

	require.NotPanics(t, func() { (*Options)(nil).Initialize() })
}

// Both credential sources set is ambiguous rather than additive: one of the
// two would silently win.
func TestValidateRejectsBothCredentialSources(t *testing.T) {
	require.ErrorIs(t,
		(&Options{InCluster: true, Kubeconfig: "/path"}).Validate(),
		ErrInClusterAndKubeconfig)

	require.NoError(t, (&Options{InCluster: true}).Validate())
	require.NoError(t, (&Options{Kubeconfig: "/path"}).Validate())
	require.NoError(t, (&Options{}).Validate(),
		"neither set is valid; Initialize resolves it to in-cluster")
	require.NoError(t, New().Validate(), "the defaults must validate")
	require.NoError(t, (*Options)(nil).Validate())
}

func TestCloneIsIndependent(t *testing.T) {
	o := &Options{InCluster: false, Kubeconfig: "/a"}
	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.Kubeconfig = "/b"
	c.InCluster = true
	require.Equal(t, "/a", o.Kubeconfig)
	require.False(t, o.InCluster)
}

func TestYAMLRoundTrip(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte("kubeconfig: /etc/kube/config\n"), &o))
	require.Equal(t, "/etc/kube/config", o.Kubeconfig)
	require.False(t, o.InCluster)
	require.NoError(t, o.Validate())

	var in Options
	require.NoError(t, yaml.Unmarshal([]byte("in_cluster: true\n"), &in))
	require.True(t, in.InCluster)
	require.NoError(t, in.Validate())
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
