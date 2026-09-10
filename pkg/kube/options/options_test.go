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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// In-cluster is the idiomatic deployment, so it is what an otherwise empty
// block means.
func TestNewDefaults(t *testing.T) {
	o := New()
	require.True(t, o.InCluster)
	require.Empty(t, o.Kubeconfig)
	require.Equal(t, DefaultQPS, o.QPS)
	require.Equal(t, DefaultBurst, o.Burst)
	require.Equal(t, timeconv.Duration(DefaultTimeout), o.Timeout)
	require.Empty(t, o.UserAgent, "the client derives the default user agent")
	require.NoError(t, o.Validate())
}

func TestInitializeDefaultsToInClusterOnlyWithoutAKubeconfig(t *testing.T) {
	o := &Options{}
	o.Initialize()
	require.True(t, o.InCluster, "an empty block means the pod's service account")
	require.Equal(t, DefaultQPS, o.QPS)
	require.Equal(t, DefaultBurst, o.Burst)
	require.Equal(t, timeconv.Duration(DefaultTimeout), o.Timeout)

	// a kubeconfig is an explicit choice to run outside the cluster, so
	// Initialize must not override it into an invalid pairing
	ext := &Options{Kubeconfig: "/path/to/kubeconfig"}
	ext.Initialize()
	require.False(t, ext.InCluster)
	require.NoError(t, ext.Validate(),
		"Initialize must not create the combination Validate rejects")

	require.NotPanics(t, func() { (*Options)(nil).Initialize() })
}

func TestInitializeKeepsExplicitTuning(t *testing.T) {
	o := &Options{InCluster: true, QPS: 5, Burst: 7,
		Timeout: timeconv.Duration(time.Second), UserAgent: "custom/1"}
	o.Initialize()
	require.Equal(t, float32(5), o.QPS)
	require.Equal(t, 7, o.Burst)
	require.Equal(t, timeconv.Duration(time.Second), o.Timeout)
	require.Equal(t, "custom/1", o.UserAgent)
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

func TestValidateRejectsNegativeTuning(t *testing.T) {
	require.ErrorIs(t, (&Options{QPS: -1}).Validate(), ErrNegativeQPS)
	require.ErrorIs(t, (&Options{Burst: -1}).Validate(), ErrNegativeBurst)
	require.ErrorIs(t, (&Options{Timeout: -1}).Validate(), ErrNegativeTimeout)
	// zero means "take the default", so it is not an error
	require.NoError(t, (&Options{QPS: 0, Burst: 0, Timeout: 0}).Validate())
}

func TestCloneIsIndependent(t *testing.T) {
	o := &Options{InCluster: false, Kubeconfig: "/a", QPS: 1, Burst: 2,
		UserAgent: "a/1", Timeout: timeconv.Duration(time.Second)}
	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.Kubeconfig = "/b"
	c.InCluster = true
	c.QPS = 9
	c.UserAgent = "b/1"
	require.Equal(t, "/a", o.Kubeconfig)
	require.False(t, o.InCluster)
	require.Equal(t, float32(1), o.QPS)
	require.Equal(t, "a/1", o.UserAgent)
}

func TestYAMLRoundTrip(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte("kubeconfig: /etc/kube/config\n"), &o))
	require.Equal(t, "/etc/kube/config", o.Kubeconfig)
	require.False(t, o.InCluster)
	require.NoError(t, o.Validate())

	var in Options
	require.NoError(t, yaml.Unmarshal([]byte(
		"in_cluster: true\nqps: 50\nburst: 100\nuser_agent: t/1\ntimeout: 5s\n"), &in))
	require.True(t, in.InCluster)
	require.Equal(t, float32(50), in.QPS)
	require.Equal(t, 100, in.Burst)
	require.Equal(t, "t/1", in.UserAgent)
	require.Equal(t, timeconv.Duration(5*time.Second), in.Timeout)
	require.NoError(t, in.Validate())

	// the tuning knobs are omitempty, so an untouched block round trips to
	// exactly what the operator wrote
	b, err := yaml.Marshal(&Options{InCluster: true})
	require.NoError(t, err)
	require.Equal(t, "in_cluster: true\n", string(b))
}
