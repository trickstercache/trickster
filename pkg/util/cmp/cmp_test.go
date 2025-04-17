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

package cmp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEq(t *testing.T) {
	cases := []struct {
		name string
		x    any
		y    any
		want bool
	}{
		// happy paths
		{
			name: "ints-equal",
			x:    1,
			y:    1,
			want: true,
		},
		{
			name: "ints-not-equal",
			x:    1,
			y:    2,
			want: false,
		},
		{
			name: "int64-equal",
			x:    int64(1),
			y:    int64(1),
			want: true,
		},
		{
			name: "int64-not-equal",
			x:    int64(1),
			y:    int64(2),
			want: false,
		},
		{
			name: "floats-equal",
			x:    2.0,
			y:    2.0,
			want: true,
		},
		{
			name: "floats-not-equal",
			x:    2.0,
			y:    2.1,
			want: false,
		},
		{
			name: "float32-equal",
			x:    float32(2.0),
			y:    float32(2.0),
			want: true,
		},
		{
			name: "float32-not-equal",
			x:    float32(2.0),
			y:    float32(2.1),
			want: false,
		},
		{
			name: "float64-equal",
			x:    float64(2.0),
			y:    float64(2.0),
			want: true,
		},
		{
			name: "float64-not-equal",
			x:    float64(2.0),
			y:    float64(2.1),
			want: false,
		},
		{
			name: "strings-equal",
			x:    "foo",
			y:    "foo",
			want: true,
		},
		{
			name: "strings-not-equal",
			x:    "foo",
			y:    "bar",
			want: false,
		},
		{
			name: "bools-equal",
			x:    true,
			y:    true,
			want: true,
		},
		{
			name: "bools-not-equal",
			x:    true,
			y:    false,
			want: false,
		},
		// unhappy paths
		{
			name: "int-float-not-equal",
			x:    1,
			y:    1.0,
			want: false,
		},
		{
			name: "float-string-not-equal",
			x:    1.0,
			y:    "1.0",
			want: false,
		},
	}

	for i := range cases {
		tc := cases[i]
		t.Run(tc.name, func(t *testing.T) {
			got := Equal(tc.x, tc.y)
			require.Equal(t, tc.want, got)
		})
	}
}
