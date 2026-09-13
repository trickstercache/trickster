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

package hostnames

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		in   string
		mode Mode
		out  string
		err  error
	}{
		{"", AllowEmpty, "", nil},
		{"  ", AllowEmpty, "", nil},
		{"", RequireHost, "", ErrEmpty},
		{"", RequirePrecise, "", ErrEmpty},
		{" Example.COM. ", RequireHost, "example.com", nil},
		{"api example.com", RequireHost, "", ErrWhitespace},
		{"*.Example.com", RequireHost, "*.example.com", nil},
		{"**.Example.com", RequireHost, "**.example.com", nil},
		{"*.example.com", RequirePrecise, "", ErrPrecise},
		{"**.example.com", RequirePrecise, "", ErrPrecise},
		{"*", RequireHost, "", ErrWildcard},
		{"*.", RequireHost, "", ErrWildcard},
		{"**.", RequireHost, "", ErrWildcard},
		{"*..example.com", RequireHost, "", ErrWildcard},
		{"***.example.com", RequireHost, "", ErrWildcard},
		{"*.*.example.com", RequireHost, "", ErrWildcard},
		{"api.*.example.com", RequireHost, "", ErrWildcard},
		{"*example.com", RequireHost, "", ErrWildcard},
		{"api.*", RequireHost, "", ErrWildcard},
		{"example.com", RequirePrecise, "example.com", nil},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			out, err := Normalize(tt.in, tt.mode)
			require.ErrorIs(t, err, tt.err)
			require.Equal(t, tt.out, out)
		})
	}
}

func TestClassification(t *testing.T) {
	require.True(t, IsWildcard("*.example.com"))
	require.True(t, IsWildcard("**.example.com"))
	require.False(t, IsWildcard("example.com"))
	require.True(t, IsAnyDepth("**.example.com"))
	require.False(t, IsAnyDepth("*.example.com"))
	require.Equal(t, "example.com", Suffix("*.example.com"))
	require.Equal(t, "example.com", Suffix("**.example.com"))
	require.Equal(t, "example.com", Suffix("example.com"))
	require.Equal(t, "**.example.com", ToAnyDepth("*.example.com"))
	require.Equal(t, "**.example.com", ToAnyDepth("**.example.com"))
	require.Equal(t, "example.com", ToAnyDepth("example.com"))
}
