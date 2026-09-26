/*
 * Copyright 2026 The Trickster Authors
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
package types

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestStatusRangesYAML(t *testing.T) {
	var l StatusRanges
	require.NoError(t, yaml.Unmarshal([]byte("[{start: 200, end: 299}, 304, {start: 405, end: 405}]"), &l))
	require.Equal(t, StatusRanges{{200, 299}, {304, 304}, {405, 405}}, l)

	// a list of bare codes, the only form that existed before ranges, round-trips unchanged
	var codes StatusRanges
	require.NoError(t, yaml.Unmarshal([]byte("[200, 204]"), &codes))
	require.Equal(t, StatusCodes(200, 204), codes)
	out, err := yaml.Marshal(codes)
	require.NoError(t, err)
	require.Equal(t, "- 200\n- 204\n", string(out))

	out, err = yaml.Marshal(l)
	require.NoError(t, err)
	var again StatusRanges
	require.NoError(t, yaml.Unmarshal(out, &again))
	require.Equal(t, l, again)
	require.Contains(t, string(out), "- 304\n")
	require.Contains(t, string(out), "- 405\n", "a single-code range marshals as a bare code")
	require.Contains(t, string(out), "start: 200")

	require.Error(t, yaml.Unmarshal([]byte("[ok]"), &again))
	require.Error(t, yaml.Unmarshal([]byte("[{start: low}]"), &again))
}

func TestStatusRangesValidate(t *testing.T) {
	require.NoError(t, StatusRanges(nil).Validate())
	require.NoError(t, StatusRanges{{100, 599}, {200, 200}}.Validate())
	for name, l := range map[string]StatusRanges{
		"below 100":   {{99, 200}},
		"above 599":   {{200, 600}},
		"backwards":   {{300, 200}},
		"zero start":  {{0, 200}},
		"second item": {{200, 299}, {700, 700}},
	} {
		require.ErrorIs(t, l.Validate(), ErrInvalidStatusRange, name)
	}
}

func TestStatusTable(t *testing.T) {
	// overlapping and adjacent ranges are one set
	table := StatusRanges{{200, 250}, {240, 299}, {300, 304}, {429, 429}}.Compile()
	for _, code := range []int{200, 245, 299, 300, 304, 429} {
		require.True(t, table.Contains(code), code)
	}
	for _, code := range []int{-1, 0, 99, 199, 305, 428, 430, 599, 600, 100000} {
		require.False(t, table.Contains(code), code)
	}
	// out-of-range bounds are clipped rather than indexed
	wide := StatusRanges{{-5, 120}, {590, 9000}}.Compile()
	require.True(t, wide.Contains(100))
	require.True(t, wide.Contains(599))
	require.False(t, wide.Contains(99))
	require.False(t, StatusRanges(nil).Compile().Contains(200))
	var none *StatusTable
	require.False(t, none.Contains(200))
	require.Zero(t, testing.AllocsPerRun(100, func() { _ = table.Contains(204) }))
}

func FuzzStatusRangesUnmarshal(f *testing.F) {
	f.Add("[200, 204]")
	f.Add("[{start: 200, end: 299}, 304]")
	f.Add("[{start: 9, end: -1}]")
	f.Add("{}")
	f.Fuzz(func(t *testing.T, doc string) {
		var l StatusRanges
		if err := yaml.Unmarshal([]byte(doc), &l); err != nil {
			return
		}
		// whatever parsed must compile without indexing out of range, valid or not
		table := l.Compile()
		if l.Validate() != nil {
			return
		}
		for _, r := range l {
			if !table.Contains(r.Start) || !table.Contains(r.End) {
				t.Errorf("valid range %d-%d is missing from its table", r.Start, r.End)
			}
		}
	})
}
