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

package headers

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAndSpellUpdateKey(t *testing.T) {
	tests := []struct {
		key  string
		op   UpdateOp
		name string
	}{
		{"", UpdateSet, ""},
		{"X-A", UpdateSet, "X-A"},
		{"+X-A", UpdateAppend, "X-A"},
		{"-X-A", UpdateDelete, "X-A"},
		{"-", UpdateDelete, ""},
		{"+-X", UpdateAppend, "-X"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			op, name := ParseUpdateKey(tt.key)
			require.Equal(t, tt.op, op)
			require.Equal(t, tt.name, name)
			require.Equal(t, tt.key, UpdateKey(op, name))
		})
	}
	require.Equal(t, "X-A", UpdateKey(UpdateOp(99), "X-A"))
}

func TestValidUpdate(t *testing.T) {
	require.NoError(t, ValidUpdate(UpdateSet, "X-A", "v"))
	require.NoError(t, ValidUpdate(UpdateAppend, "X-A", "v"))
	require.NoError(t, ValidUpdate(UpdateDelete, "X-A", ""))
	require.NoError(t, ValidUpdate(UpdateDelete, "X-A", "\x00 ignored on delete"))

	for _, name := range []string{"", "-X", "+X", "X A", "X\x00"} {
		err := ValidUpdate(UpdateSet, name, "v")
		require.True(t, errors.Is(err, ErrInvalidHeaderName), name)
	}
	err := ValidUpdate(UpdateSet, "X-A", "bad\x00value")
	require.True(t, errors.Is(err, ErrInvalidHeaderValue))
	err = ValidUpdate(UpdateAppend, "X-A", "bad\nvalue")
	require.True(t, errors.Is(err, ErrInvalidHeaderValue))
}

func TestUpdatesFold(t *testing.T) {
	var u Updates
	require.Equal(t, 0, u.Len())
	require.Nil(t, u.Render())

	u.Set("X-Set", "a")
	u.Set("x-set", "b") // same header, any case: the last word wins
	u.Add("X-Set", "c") // joins the set value
	u.Add("X-Add", "1") // a lone add appends to the request's own
	u.Add("X-Add", "2")
	u.Remove("X-Del")
	u.Add("X-Del", "back") // after a delete an add is the whole value
	u.Set("X-Gone", "v")
	u.Remove("X-Gone")
	require.Equal(t, 4, u.Len())
	require.Equal(t, map[string]string{
		"X-Set": "b,c", "+X-Add": "1,2", "X-Del": "back", "-X-Gone": "",
	}, u.Render())
}

func TestUpdatesMerge(t *testing.T) {
	var u Updates
	u.Merge(map[string]string{"X-B": "1", "+X-B": "2", "-X-C": "", "X-A": "a"})
	// keys fold in byte order, so operator-prefixed keys fold first and the
	// bare set of the same header has the last word
	require.Equal(t, map[string]string{"X-A": "a", "X-B": "1", "-X-C": ""}, u.Render())
	u.Merge(nil)
	require.Equal(t, 3, u.Len())
}
