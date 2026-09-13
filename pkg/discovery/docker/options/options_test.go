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
	"strings"
	"testing"

	derrors "github.com/trickstercache/trickster/v2/pkg/discovery/errors"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The default is the well-known socket in the same URL form DOCKER_HOST
// takes, so an operator can paste one into the other.
func TestDefaultHostIsTheDockerHostForm(t *testing.T) {
	require.True(t, strings.HasPrefix(DefaultHost, "unix://"),
		"the default must be the socket, in DOCKER_HOST's own spelling")
	require.Contains(t, DefaultHost, "/docker.sock")
}

// The API version is pinned low rather than omitted: an unversioned request
// binds to whatever the daemon's newest version happens to be, so the
// response shape would change under Trickster when the host upgrades.
func TestDefaultAPIVersionIsPinned(t *testing.T) {
	require.Equal(t, "v1.41", DefaultAPIVersion)
	require.NoError(t, (&Options{APIVersion: DefaultAPIVersion}).Validate(),
		"the default must satisfy the format the validator enforces")
}

func TestGetAPIVersionDefaults(t *testing.T) {
	require.Equal(t, DefaultAPIVersion, (*Options)(nil).GetAPIVersion())
	require.Equal(t, DefaultAPIVersion, (&Options{}).GetAPIVersion())
	require.Equal(t, DefaultAPIVersion, New().GetAPIVersion())
	require.Equal(t, "v1.44", (&Options{APIVersion: "v1.44"}).GetAPIVersion())
}

func TestValidateAPIVersionFormat(t *testing.T) {
	for _, good := range []string{"v1.24", "v1.41", "v1.44", "v2.0", "v10.99"} {
		require.NoError(t, (&Options{APIVersion: good}).Validate(), "%q", good)
	}
	for _, bad := range []string{
		"1.41",    // missing the v prefix the Engine API path needs
		"v1",      // no minor
		"v1.41.0", // not the Engine API's two-part form
		"latest",  // not a version
		"v1.x",    // not numeric
		" v1.41",  // stray whitespace would corrupt the request path
	} {
		require.ErrorIs(t, (&Options{APIVersion: bad}).Validate(),
			ErrInvalidAPIVersion, "%q must be rejected", bad)
	}
	require.NoError(t, (&Options{}).Validate(),
		"unset means the pinned default, not an error")
	require.EqualError(t, (*Options)(nil).Validate(), "the 'docker' block is required")
}

func TestCloneIsIndependent(t *testing.T) {
	o := &Options{APIVersion: "v1.44"}
	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.APIVersion = "v1.24"
	require.Equal(t, "v1.44", o.APIVersion)
}

func TestYAMLRoundTrip(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte("api_version: v1.44\n"), &o))
	require.Equal(t, "v1.44", o.APIVersion)
	require.NoError(t, o.Validate())

	// an empty block is the common case and must round-trip
	var empty Options
	require.NoError(t, yaml.Unmarshal([]byte("{}\n"), &empty))
	require.NoError(t, empty.Validate())
	require.Equal(t, DefaultAPIVersion, empty.GetAPIVersion())
}

func TestNewErrInvalidOptions(t *testing.T) {
	err := NewErrInvalidOptions("containers", "'api_version' must look like 'v1.41'")
	require.EqualError(t, err,
		`invalid docker options for discoverer "containers": `+
			`'api_version' must look like 'v1.41'`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target))
}
