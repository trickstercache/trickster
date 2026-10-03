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

package ipacl

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"

	"github.com/stretchr/testify/require"
)

func TestLookupValidate(t *testing.T) {
	warnings, err := Lookup{
		"office": {Allow: []string{"10.0.0.0/8"}},
		"None":   {Allow: []string{"192.0.2.10"}},
	}.Validate()
	require.NoError(t, err)
	require.Empty(t, warnings)

	_, err = Lookup{"": {Allow: []string{"10.0.0.0/8"}}}.Validate()
	require.ErrorIs(t, err, ErrInvalidName)

	_, err = Lookup{reserved.ReferenceNone: {Allow: []string{"10.0.0.0/8"}}}.Validate()
	require.ErrorIs(t, err, ErrInvalidName)

	_, err = Lookup{"bad": nil}.Validate()
	require.ErrorIs(t, err, ErrInvalidName)
}

func TestLookupValidateCompileErrors(t *testing.T) {
	cases := []struct {
		name    string
		options Options
		target  error
	}{
		{"match", Options{Match: "first", Allow: []string{"10.0.0.0/8"}}, ErrInvalidMatch},
		{"default", Options{Default: "reject", Allow: []string{"10.0.0.0/8"}}, ErrInvalidDefault},
		{"source", Options{Source: "socket", Allow: []string{"10.0.0.0/8"}}, ErrInvalidSource},
		{"action", Options{Action: "close", Allow: []string{"10.0.0.0/8"}}, ErrInvalidAction},
		{"status", Options{Status: 200, Allow: []string{"10.0.0.0/8"}}, ErrInvalidStatus},
		{"entry", Options{Allow: []string{"not-an-address"}}, ErrInvalidEntry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Lookup{"office": &tc.options}.Validate()
			require.ErrorIs(t, err, tc.target)
		})
	}
}

func TestLookupValidateFilesAndWarnings(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.lst")
	_, err := Lookup{"office": {AllowFile: missing}}.Validate()
	require.ErrorIs(t, err, ErrInvalidFile)
	require.ErrorIs(t, err, os.ErrNotExist)

	path := writeFile(t, "10.0.0.0/8\n")
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if f, openErr := os.Open(path); openErr == nil {
		_ = f.Close()
		t.Skip("this process can read a mode 000 file")
	}
	_, err = Lookup{"office": {AllowFile: path}}.Validate()
	require.ErrorIs(t, err, ErrInvalidFile)
	require.ErrorIs(t, err, os.ErrPermission)
}

func TestLookupValidateWarningsAndClone(t *testing.T) {
	lookup := Lookup{"closed": {}}
	warnings, err := lookup.Validate()
	require.NoError(t, err)
	require.Equal(t, []string{
		`ip acl "closed": no entries and default deny; every address is denied`,
	}, warnings)
	require.NotNil(t, lookup["closed"].Compiled)
	require.Equal(t, "closed", lookup["closed"].Name)

	cloned := lookup.Clone()
	require.Equal(t, lookup["closed"].Compiled, cloned["closed"].Compiled)
	cloned["closed"].Allow = []string{"10.0.0.0/8"}
	require.Empty(t, lookup["closed"].Allow)

	var none Lookup
	require.Nil(t, none.Clone())
	require.Nil(t, (*Options)(nil).Clone())
}

func TestLookupValidateStopsOnFirstError(t *testing.T) {
	_, err := Lookup{
		"office": {Allow: []string{"not-an-address"}},
	}.Validate()
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrInvalidName))
}
