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
	require.Equal(t, []string{ServiceEC2, ServiceECS}, SupportedServices())
	require.NotEqual(t, ServiceEC2, ServiceECS)
}

// service is required with no default: with more than one AWS API
// supported, picking one for the operator would be an arbitrary guess.
func TestValidateRequiresService(t *testing.T) {
	require.Empty(t, New().Service, "there is no default service")

	err := (&Options{Region: "us-east-1"}).Validate()
	require.ErrorIs(t, err, ErrMissingService)
	require.Contains(t, err.Error(), ServiceEC2)
	require.Contains(t, err.Error(), ServiceECS,
		"the message must name every service that would work")

	require.ErrorIs(t,
		(&Options{Service: "s3", Region: "us-east-1"}).Validate(),
		ErrInvalidService)
	// naming the resource rather than the service is the likely mistake
	require.ErrorIs(t,
		(&Options{Service: "instances", Region: "us-east-1"}).Validate(),
		ErrInvalidService)

	// an absent block and a present-but-incomplete one are different
	// mistakes, and get different messages
	require.EqualError(t, (*Options)(nil).Validate(), "the 'aws' block is required")
}

func TestValidateAcceptsEverySupportedService(t *testing.T) {
	for _, s := range SupportedServices() {
		require.NoError(t, (&Options{Service: s, Region: "us-east-1"}).Validate(),
			"%s is advertised as supported and must validate", s)
	}
}

func TestGetService(t *testing.T) {
	require.Empty(t, (*Options)(nil).GetService())
	require.Empty(t, (&Options{}).GetService())
	require.Equal(t, ServiceECS, (&Options{Service: ServiceECS}).GetService())
}

// The discovery service doubles as the SigV4 signing service name -- one
// field, both meanings, which only works because they coincide.
func TestSignerOptionsCarryTheServiceAndCredentials(t *testing.T) {
	require.Nil(t, (*Options)(nil).SignerOptions())

	o := &Options{
		Service: ServiceEC2, Region: "eu-west-2",
		AccessKey: "AKIA", SecretKey: "shh",
		Profile: "prod", RoleARN: "arn:aws:iam::1:role/R",
	}
	so := o.SignerOptions()
	require.Equal(t, ServiceEC2, so.Service,
		"the signing service is derived from the discovery service")
	require.Equal(t, "eu-west-2", so.Region)
	require.Equal(t, "AKIA", so.AccessKey)
	require.Equal(t, "shh", string(so.SecretKey))
	require.Equal(t, "prod", so.Profile)
	require.Equal(t, "arn:aws:iam::1:role/R", so.RoleARN)
}

func TestCloneIsIndependent(t *testing.T) {
	o := &Options{Service: ServiceEC2, Region: "us-east-1", AccessKey: "A"}
	c := o.Clone()
	require.Equal(t, o, c)
	require.NotSame(t, o, c)

	c.Region = "us-west-2"
	require.Equal(t, "us-east-1", o.Region)
}

// The credential is redacted on the way out, so a config dump or the
// management API cannot emit it.
func TestSecretKeyIsRedactedOnMarshal(t *testing.T) {
	out, err := yaml.Marshal(&Options{
		Service: ServiceEC2, AccessKey: "AKIA", SecretKey: "super-secret"})
	require.NoError(t, err)
	require.NotContains(t, string(out), "super-secret")
	require.Contains(t, string(out), "AKIA",
		"only the secret half of the pair is redacted")
}

func TestYAMLRoundTrip(t *testing.T) {
	const doc = `
service: ecs
region: us-east-1
access_key: AKIA
secret_key: shh
profile: prod
role_arn: arn:aws:iam::1:role/R
`
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(doc), &o))
	require.Equal(t, ServiceECS, o.Service)
	require.Equal(t, "us-east-1", o.Region)
	require.Equal(t, "shh", string(o.SecretKey),
		"unmarshaling keeps the value; only output is redacted")
	require.NoError(t, o.Validate())
}

func TestNewErrInvalidOptions(t *testing.T) {
	err := NewErrInvalidOptions("fleet", "'service' is required")
	require.EqualError(t, err,
		`invalid aws options for discoverer "fleet": 'service' is required`)

	var target *derrors.InvalidDiscoveryOptionsError
	require.True(t, errors.As(err, &target))
}
