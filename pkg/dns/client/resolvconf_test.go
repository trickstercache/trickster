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

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testResolvConf = `# a comment
domain example.com
search example.com example.net
nameserver 10.0.0.53
	nameserver	10.0.0.54	; trailing comment
nameserver fe80::1%eth0
nameserver not-an-address
nameserver
options ndots:2
`

func TestParseResolvConf(t *testing.T) {
	rc, err := parseResolvConf(strings.NewReader(testResolvConf))
	require.NoError(t, err)
	require.Equal(t, []string{
		"10.0.0.53:53",
		"10.0.0.54:53",
		"[fe80::1%eth0]:53",
	}, rc.Servers, "only parsable nameserver addresses are kept, with the default port")
}

func TestParseResolvConfNoServers(t *testing.T) {
	_, err := parseResolvConf(strings.NewReader("search example.com\n"))
	require.ErrorIs(t, err, ErrNoServers)
}

func TestLoadResolvConf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	require.NoError(t, os.WriteFile(path, []byte(testResolvConf), 0o600))
	rc, err := LoadResolvConf(path)
	require.NoError(t, err)
	require.Equal(t, "10.0.0.53:53", rc.Servers[0])

	_, err = LoadResolvConf(filepath.Join(t.TempDir(), "absent.conf"))
	require.Error(t, err)
}

func TestRCodeString(t *testing.T) {
	require.Equal(t, "NOERROR", RCodeSuccess.String())
	require.Equal(t, "SERVFAIL", RCodeServerFailure.String())
	require.Equal(t, "NXDOMAIN", RCodeNameError.String())
	require.Equal(t, "RCODE99", RCode(99).String())
}
