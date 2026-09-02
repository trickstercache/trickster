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

package secret

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	// the same yaml package the config loader uses
	"go.yaml.in/yaml/v3"
)

const value = "super-secret-value"

// The whole point of the type: a config dump must not carry the value.
func TestMarshalYAMLRedacts(t *testing.T) {
	type holder struct {
		Password Secret `yaml:"password"`
	}
	out, err := yaml.Marshal(holder{Password: value})
	require.NoError(t, err)
	require.NotContains(t, string(out), value)
	require.Contains(t, string(out), Token)
}

func TestMarshalJSONRedacts(t *testing.T) {
	type holder struct {
		Password Secret `json:"password"`
	}
	out, err := json.Marshal(holder{Password: value})
	require.NoError(t, err)
	require.NotContains(t, string(out), value)
	require.JSONEq(t, `{"password":"`+Token+`"}`, string(out))
}

// String() is the leak that is easiest to introduce by accident: a secret
// interpolated into a log line or an error message.
func TestStringRedacts(t *testing.T) {
	s := Secret(value)
	require.Equal(t, Token, s.String())
	require.NotContains(t, fmt.Sprintf("%s", s), value)
	require.NotContains(t, fmt.Sprintf("%v", s), value)
	require.NotContains(t, fmt.Sprint(s), value)
}

// An empty secret is absent rather than redacted: emitting "<secret>" for a
// credential that was never set would make an unset field look configured.
func TestEmptyIsAbsentNotRedacted(t *testing.T) {
	var empty Secret
	require.Empty(t, empty.String())

	y, err := yaml.Marshal(struct {
		Password Secret `yaml:"password,omitempty"`
	}{})
	require.NoError(t, err)
	require.NotContains(t, string(y), Token)

	j, err := json.Marshal(struct {
		Password Secret `json:"password"`
	}{})
	require.NoError(t, err)
	require.JSONEq(t, `{"password":null}`, string(j))

	// called directly, since a struct tagged omitempty never reaches the
	// marshaler at all and would leave this branch untested
	v, err := empty.MarshalYAML()
	require.NoError(t, err)
	require.Nil(t, v, "an unset secret marshals to nothing, not to a token")
}

// Redaction is one-way by design, but the value must still be usable by the
// code that needs it -- otherwise callers would reach for a plain string.
func TestValueIsStillReadableByConversion(t *testing.T) {
	s := Secret(value)
	require.Equal(t, value, string(s),
		"an explicit conversion is how a caller signs a request with it")
}

// Unmarshaling is deliberately not customized: a config file supplies the
// real value, and only output is redacted.
func TestUnmarshalKeepsTheValue(t *testing.T) {
	var h struct {
		Password Secret `yaml:"password"`
	}
	require.NoError(t, yaml.Unmarshal([]byte("password: "+value+"\n"), &h))
	require.Equal(t, Secret(value), h.Password)

	// and a round trip redacts on the way back out
	out, err := yaml.Marshal(h)
	require.NoError(t, err)
	require.NotContains(t, string(out), value)
}
