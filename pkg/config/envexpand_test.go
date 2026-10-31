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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func testExpander(env map[string]string) *envExpander {
	e := newEnvExpander()
	e.lookup = func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
	return e
}

func TestEnvExpand(t *testing.T) {
	env := map[string]string{
		"FOO": "bar", "BAR": "baz", "NESTED": "${FOO}", "_U9": "u", "EMPTY": "",
		"TRK_X": "trk",
	}
	tests := []struct {
		name, in, prefix, want string
		unset                  bool
	}{
		{name: "empty", in: "", want: ""},
		{name: "no reference", in: "plain", want: "plain"},
		{name: "single", in: "${FOO}", want: "bar"},
		{name: "adjacent", in: "${FOO}${BAR}", want: "barbaz"},
		{name: "embedded", in: "basic jdoe:${FOO}!", want: "basic jdoe:bar!"},
		{name: "underscore and digits", in: "${_U9}", want: "u"},
		{name: "whole unset", in: "${NOPE}", want: "", unset: true},
		{name: "embedded unset", in: "a${NOPE}b", want: "ab"},
		{name: "set but empty", in: "${EMPTY}", want: ""},
		{name: "value not re-expanded", in: "${NESTED}", want: "${FOO}"},
		{name: "bcrypt hash", in: "$2y$10$abc/def.ghi", want: "$2y$10$abc/def.ghi"},
		{name: "bare reference", in: "$FOO", want: "$FOO"},
		{name: "trailing open", in: "x${", want: "x${"},
		{name: "unterminated", in: "${FOO", want: "${FOO"},
		{name: "empty name", in: "${}", want: "${}"},
		{name: "leading digit", in: "${1}", want: "${1}"},
		{name: "unknown operator", in: "${FOO-x}${FOO}", want: "${FOO-x}bar"},
		{name: "nested is literal", in: "${A:-${FOO}}", want: "${A:-bar}"},
		{name: "escaped", in: "$${FOO}", want: "${FOO}"},
		{name: "escaped then reference", in: "$${FOO}${BAR}", want: "${FOO}baz"},
		{name: "dollar before escape", in: "$$${FOO}", want: "$${FOO}"},
		{name: "double dollar", in: "a$$b", want: "a$$b"},
		{name: "default unused", in: "${FOO:-d}", want: "bar"},
		{name: "default when unset", in: "${NOPE:-d e}", want: "d e"},
		{name: "default when empty", in: "${EMPTY:-d}", want: "d"},
		{name: "empty default", in: "${NOPE:-}", want: ""},
		{name: "default in text", in: "http://${NOPE:-localhost}:9090", want: "http://localhost:9090"},
		{name: "required set", in: "${FOO:?missing}", want: "bar"},
		{name: "prefix match", in: "${TRK_X}", prefix: rewriterEnvPrefix, want: "trk"},
		{
			name: "prefix skips capture", in: "/${site}/${1}/${TRK_X}", prefix: rewriterEnvPrefix,
			want: "/${site}/${1}/trk",
		},
		{name: "prefix unset", in: "${TRK_NOPE}", prefix: rewriterEnvPrefix, want: "", unset: true},
		{name: "prefix default", in: "${TRK_NOPE:-d}", prefix: rewriterEnvPrefix, want: "d"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, unset, err := testExpander(env).expand(test.in, test.prefix)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
			require.Equal(t, test.unset, unset)
		})
	}
}

func TestEnvExpandRequired(t *testing.T) {
	e := testExpander(map[string]string{"EMPTY": ""})
	_, _, err := e.expand("${NOPE:?set it to the API token}", "")
	require.EqualError(t, err, "environment variable NOPE is required: set it to the API token")
	_, _, err = e.expand("x${EMPTY:?}", "")
	require.EqualError(t, err, "environment variable EMPTY is required")
	require.Empty(t, e.warnings())
}

func TestEnvExpandWarnings(t *testing.T) {
	e := testExpander(nil)
	for _, in := range []string{"${B}", "a${A}", "${B}", "${C:-d}"} {
		_, _, err := e.expand(in, "")
		require.NoError(t, err)
	}
	require.Equal(t, []string{
		"config references environment variable A, which is not set",
		"config references environment variable B, which is not set",
	}, e.warnings())
}

const envExpandDocument = `
port: ${PORT}
quoted_port: "${PORT}"
enabled: ${ON}
ratio: ${RATIO}
ttl: ${TTL}
name: ${NULLISH}
tagged: !!str ${PORT}
dropped: ${NOPE}
${KEY}: kept
list:
  - ${NOPE}
  - ${PORT}
request_rewriters:
  rw:
    instructions:
      - [ 'hostname', 'set', '${site}.${TRK_DOMAIN}' ]
      - [ 'path', 'set', '${PORT}' ]
    other: ${PORT}
`

func TestEnvExpandDocument(t *testing.T) {
	e := testExpander(map[string]string{
		"PORT": "8480", "ON": "true", "RATIO": "0.5", "TTL": "5s", "NULLISH": "null", "KEY": "k",
		"TRK_DOMAIN": "example.com", "site": "leaked",
	})
	doc := &yaml.Node{}
	require.NoError(t, yaml.Unmarshal([]byte(envExpandDocument), doc))
	changed, err := e.expandDocument(doc)
	require.NoError(t, err)
	require.True(t, changed)
	var out struct {
		Port       int           `yaml:"port"`
		QuotedPort string        `yaml:"quoted_port"`
		Enabled    bool          `yaml:"enabled"`
		Ratio      float64       `yaml:"ratio"`
		TTL        time.Duration `yaml:"ttl"`
		Name       string        `yaml:"name"`
		Tagged     string        `yaml:"tagged"`
		Dropped    *string       `yaml:"dropped"`
		Key        string        `yaml:"${KEY}"`
		List       []string      `yaml:"list"`
		Rewriters  map[string]struct {
			Instructions [][]string `yaml:"instructions"`
			Other        int        `yaml:"other"`
		} `yaml:"request_rewriters"`
	}
	require.NoError(t, doc.Decode(&out))
	require.Equal(t, 8480, out.Port)
	require.Equal(t, "8480", out.QuotedPort)
	require.True(t, out.Enabled)
	require.InDelta(t, 0.5, out.Ratio, 0)
	require.Equal(t, 5*time.Second, out.TTL)
	require.Equal(t, "null", out.Name, "a value that reads as null stays a string")
	require.Equal(t, "8480", out.Tagged)
	require.Nil(t, out.Dropped, "a single unset reference leaves the key unset")
	require.Equal(t, "kept", out.Key, "keys are never expanded")
	require.Equal(t, []string{"", "8480"}, out.List, "a sequence keeps its positions")
	rw := out.Rewriters["rw"]
	require.Equal(t, [][]string{
		{"hostname", "set", "${site}.example.com"},
		{"path", "set", "${PORT}"},
	}, rw.Instructions, "only TRK_ names expand in instructions")
	require.Equal(t, 8480, rw.Other)

	// the merged-text path re-parses the marshaled document, which must decode the same way
	data, err := yaml.Marshal(doc)
	require.NoError(t, err)
	var again struct {
		Port int    `yaml:"port"`
		Name string `yaml:"name"`
	}
	require.NoError(t, yaml.Unmarshal(data, &again))
	require.Equal(t, 8480, again.Port)
	require.Equal(t, "null", again.Name)

	// an unset reference on a non-string field is a decode error rather than a silent zero
	doc = &yaml.Node{}
	require.NoError(t, yaml.Unmarshal([]byte("list:\n  - ${NOPE}\n"), doc))
	_, err = testExpander(nil).expandDocument(doc)
	require.NoError(t, err)
	var ints struct {
		List []int `yaml:"list"`
	}
	require.Error(t, doc.Decode(&ints))
}

func TestEnvExpandDocumentErrors(t *testing.T) {
	doc := &yaml.Node{}
	require.NoError(t, yaml.Unmarshal([]byte("a:\n  - b: ${NOPE:?needed}\n"), doc))
	_, err := testExpander(nil).expandDocument(doc)
	require.EqualError(t, err, "line 2: environment variable NOPE is required: needed")
	for _, in := range []string{"a:\n  - ${NOPE:?}\n", "a: {b: [{c: '${NOPE:?}'}]}\n", "${NOPE:?}\n"} {
		doc = &yaml.Node{}
		require.NoError(t, yaml.Unmarshal([]byte(in), doc))
		_, err = testExpander(nil).expandDocument(doc)
		require.Error(t, err, in)
	}
	changed, err := testExpander(nil).expandDocument(nil)
	require.NoError(t, err)
	require.False(t, changed)
	// a bare scalar node expands like a document's
	n := &yaml.Node{Kind: yaml.ScalarNode, Value: "${A}"}
	changed, err = testExpander(map[string]string{"A": "1"}).expandDocument(n)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "1", n.Value)
}
