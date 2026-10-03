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
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "list")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestFileLongest(t *testing.T) {
	allow := writeFile(t, `
# office
10.0.0.0/8

  # indented comment
  192.0.2.10
2001:db8::/32
`)
	deny := writeFile(t, "10.1.2.0/24\n")
	list, warnings := mustCompile(t, Options{AllowFile: allow, DenyFile: deny})
	require.Empty(t, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.1.2.1")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.9.9.9")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("192.0.2.10")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("2001:db8::1")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("192.0.2.11")))
}

func TestFileOrderedBetweenRules(t *testing.T) {
	path := writeFile(t, "# partners\n192.0.2.10\n2001:db8::/32\n")
	list, warnings := mustCompile(t, Options{
		Match: "ordered",
		Rules: []Rule{
			{Deny: "10.1.2.0/24"},
			{Allow: "10.0.0.0/8"},
			{AllowFile: path},
			{Deny: "all"},
			{Allow: "192.0.2.11"},
		},
	})
	require.Equal(t, []string{
		`ip acl: unreachable rule "192.0.2.11" for 192.0.2.11/32 (rule 5 allow)`,
	}, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.1.2.1")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.9.1.1")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("192.0.2.10")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("2001:db8::5")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("192.0.2.11")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("2001:db9::1")))
}

func TestFileDenyOrdered(t *testing.T) {
	path := writeFile(t, "10.1.2.0/24\n")
	list, warnings := mustCompile(t, Options{
		Match:   "ordered",
		Default: "allow",
		Rules:   []Rule{{DenyFile: path}},
	})
	require.Empty(t, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.1.2.8")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.9.9.9")))
}

func TestFileAllowAfterDeny(t *testing.T) {
	path := writeFile(t, "10.1.2.0/24\n")
	list, warnings := mustCompile(t, Options{
		Deny:      []string{"10.1.2.0/24"},
		AllowFile: path,
	})
	require.Equal(t, []string{
		`ip acl: 10.1.2.0/24 is listed as both allow and deny; deny wins (allow_file "` + path + `" line 1)`,
	}, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.1.2.9")))
}

func TestFileTrailingComment(t *testing.T) {
	path := writeFile(t, "10.0.0.0/8 # office\n")
	_, _, err := Compile(Options{AllowFile: path})
	require.ErrorIs(t, err, ErrInvalidEntry)
	require.ErrorContains(t, err, "line 1")
	require.ErrorContains(t, err, "10.0.0.0/8 # office")
}

func TestFileErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.lst")
	_, _, err := Compile(Options{AllowFile: missing})
	require.ErrorIs(t, err, ErrInvalidFile)
	require.ErrorIs(t, err, os.ErrNotExist)

	_, _, err = Compile(Options{DenyFile: t.TempDir()})
	require.ErrorIs(t, err, ErrInvalidFile)
	require.ErrorContains(t, err, "directory")

	bad := writeFile(t, "# ok\n\n10.0.0.0/8\nnot-an-address\n")
	_, _, err = Compile(Options{AllowFile: bad})
	require.ErrorIs(t, err, ErrInvalidEntry)
	require.ErrorContains(t, err, "line 4")
	require.ErrorContains(t, err, "not-an-address")

	_, _, err = Compile(Options{
		Match: "ordered",
		Rules: []Rule{{AllowFile: bad}},
	})
	require.ErrorIs(t, err, ErrInvalidEntry)
	require.ErrorContains(t, err, "rule 1")
	require.ErrorContains(t, err, "line 4")
}

func TestFileUnreadable(t *testing.T) {
	path := writeFile(t, "10.0.0.0/8\n")
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() {
		_ = os.Chmod(path, 0o600)
	})
	if f, openErr := os.Open(path); openErr == nil {
		_ = f.Close()
		t.Skip("this process can read a mode 000 file")
	}
	_, _, err := Compile(Options{AllowFile: path})
	require.ErrorIs(t, err, ErrInvalidFile)
	require.ErrorIs(t, err, os.ErrPermission)
}

func TestFileEmpty(t *testing.T) {
	path := writeFile(t, "# nothing\n\n")
	list, warnings := mustCompile(t, Options{Name: "closed", AllowFile: path})
	require.Equal(t, []string{
		`ip acl "closed": no entries and default deny; every address is denied`,
	}, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.0.0.1")))

	list, warnings = mustCompile(t, Options{AllowFile: path, Default: "allow"})
	require.Empty(t, warnings)
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.0.0.1")))
	require.Equal(t, Deny, list.Check(netip.Addr{}))
}

func TestFileAllAndDuplicate(t *testing.T) {
	path := writeFile(t, "all\n0.0.0.0/0\n")
	list, warnings := mustCompile(t, Options{AllowFile: path})
	require.Equal(t, []string{
		"ip acl: duplicate entry 0.0.0.0/0 (allow_file \"" + path + "\" line 2)",
	}, warnings)
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("192.0.2.1")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("2001:db8::1")))
}

func TestParseLinesError(t *testing.T) {
	_, err := parseLines(failReader{})
	require.ErrorContains(t, err, "read failed")
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}
