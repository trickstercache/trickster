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

package aws

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The default preserves every config written against the previous
// implementation, which could sign for nothing but Amazon Managed Service
// for Prometheus.
func TestGetServiceDefaultsToAPS(t *testing.T) {
	var nilOpts *Options
	require.Equal(t, DefaultService, nilOpts.GetService())
	require.Equal(t, DefaultService, (&Options{}).GetService())
	require.Equal(t, DefaultService, (&Options{Service: "  "}).GetService())
	require.Equal(t, "ec2", (&Options{Service: "ec2"}).GetService())
	require.Equal(t, "ec2", (&Options{Service: " ec2 "}).GetService())
}

// A half-configured static credential is a mistake, not a partial
// credential: silently falling through to the chain would authenticate as
// somebody else entirely.
func TestValidate(t *testing.T) {
	tests := map[string]struct {
		o    *Options
		want error
	}{
		"nil is valid":       {nil, nil},
		"empty is valid":     {&Options{}, nil},
		"chain only":         {&Options{Region: "us-east-1"}, nil},
		"complete static":    {&Options{AccessKey: "a", SecretKey: "s"}, nil},
		"access key alone":   {&Options{AccessKey: "a"}, ErrIncompleteStaticCredentials},
		"secret key alone":   {&Options{SecretKey: "s"}, ErrIncompleteStaticCredentials},
		"whitespace service": {&Options{Service: "   "}, ErrEmptyService},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.o.Validate()
			if test.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, test.want)
		})
	}
}

// The secret must never reach a config dump, the management API, a log, or
// an error message.
func TestSecretRedaction(t *testing.T) {
	o := &Options{Region: "us-east-1", AccessKey: "AKIA", SecretKey: "super-secret"}

	y, err := yaml.Marshal(o)
	require.NoError(t, err)
	require.NotContains(t, string(y), "super-secret")
	require.Contains(t, string(y), secretToken)

	j, err := json.Marshal(o)
	require.NoError(t, err)
	require.NotContains(t, string(j), "super-secret")

	require.NotContains(t, fmt.Sprintf("%v", o.SecretKey), "super-secret")
	require.NotContains(t, o.SecretKey.String(), "super-secret")

	// an unset secret stays absent rather than becoming a literal token
	empty := Secret("")
	require.Empty(t, empty.String())
	v, err := empty.MarshalYAML()
	require.NoError(t, err)
	require.Nil(t, v)
	jb, err := empty.MarshalJSON()
	require.NoError(t, err)
	require.Equal(t, "null", string(jb))
}

// The secret must still round-trip in from config; only marshaling is
// redacted.
func TestSecretUnmarshals(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(
		"region: us-east-1\naccess_key: AKIA\nsecret_key: super-secret\n"), &o))
	require.Equal(t, Secret("super-secret"), o.SecretKey)
}

// Clone must be deep: the previous implementation left this field shallow,
// so a cloned backend shared its credentials pointer with the original.
func TestCloneIsDeep(t *testing.T) {
	var nilOpts *Options
	require.Nil(t, nilOpts.Clone())

	o := &Options{Region: "us-east-1", Service: "ec2", SecretKey: "s"}
	c := o.Clone()
	require.NotSame(t, o, c)
	require.Equal(t, *o, *c)
	c.Region = "eu-west-1"
	c.Service = "ecs"
	require.Equal(t, "us-east-1", o.Region)
	require.Equal(t, "ec2", o.Service)
}

// The YAML keys are unchanged from the previous implementation, so existing
// configs keep working byte for byte.
func TestYAMLKeysAreUnchanged(t *testing.T) {
	const src = `
region: us-east-1
access_key: AKIA
secret_key: shh
profile: prod
role_arn: arn:aws:iam::123456789012:role/Trickster
`
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(src), &o))
	require.Equal(t, "us-east-1", o.Region)
	require.Equal(t, "AKIA", o.AccessKey)
	require.Equal(t, Secret("shh"), o.SecretKey)
	require.Equal(t, "prod", o.Profile)
	require.Equal(t, "arn:aws:iam::123456789012:role/Trickster", o.RoleARN)
	require.Equal(t, DefaultService, o.GetService(),
		"a config with no service still signs for aps, as it always did")
}
