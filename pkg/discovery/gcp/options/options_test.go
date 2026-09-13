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

func TestSupportedServices(t *testing.T) {
	require.Equal(t, []string{ServiceGCE}, SupportedServices())
}

// service is required even though 'gce' is the only value this build
// supports: a default added now could never be removed, and every later
// service would be reached by opting out of a value never chosen.
func TestValidateRequiresServiceDespiteHavingOnlyOne(t *testing.T) {
	require.Empty(t, New().Service, "there is no default service")

	err := (&Options{Project: "p"}).Validate()
	require.ErrorIs(t, err, ErrMissingService)
	require.Contains(t, err.Error(), ServiceGCE)

	require.NoError(t, (&Options{Service: ServiceGCE, Project: "p"}).Validate())
	require.EqualError(t, (*Options)(nil).Validate(), "the 'gcp' block is required")
}

// Values name the product an operator would recognize, not the API that
// serves it -- so naming the resource collection is the likely mistake and
// must fail loudly.
func TestValidateRejectsResourceAndAPINames(t *testing.T) {
	for _, bad := range []string{"instances", "compute", "GCE", "vm", "gcp"} {
		require.ErrorIs(t, (&Options{Service: bad, Project: "p"}).Validate(),
			ErrInvalidService, "%q must not be accepted", bad)
	}
}

func TestGetService(t *testing.T) {
	require.Empty(t, (*Options)(nil).GetService())
	require.Empty(t, (&Options{}).GetService())
	require.Equal(t, ServiceGCE, (&Options{Service: ServiceGCE}).GetService())
}

// Project is deliberately not required: on a GCE instance it comes from the
// metadata server, which is the idiomatic deployment and would be broken by
// demanding it in config.
func TestProjectIsOptionalInConfig(t *testing.T) {
	require.NoError(t, (&Options{Service: ServiceGCE}).Validate())
	require.NotNil(t, ErrMissingProject,
		"an unresolvable project surfaces at request time, not validation")
}

func TestScopeIsReadOnly(t *testing.T) {
	require.Contains(t, ComputeReadonlyScope, "readonly",
		"Trickster never mutates a project")
}

func TestCloneIsIndependent(t *testing.T) {
	o := &Options{Service: ServiceGCE, Project: "p", CredentialsFile: "/sa.json"}
	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.Project = "other"
	require.Equal(t, "p", o.Project)
}

func TestYAMLRoundTrip(t *testing.T) {
	const doc = `
service: gce
project: my-project
credentials_file: /etc/trickster/sa.json
`
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(doc), &o))
	require.Equal(t, ServiceGCE, o.Service)
	require.Equal(t, "my-project", o.Project)
	require.Equal(t, "/etc/trickster/sa.json", o.CredentialsFile)
	require.NoError(t, o.Validate())
}

func TestNewErrInvalidOptions(t *testing.T) {
	err := NewErrInvalidOptions("fleet", "'service' is required")
	require.EqualError(t, err,
		`invalid gcp options for discoverer "fleet": 'service' is required`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target))
}
