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
package options

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/secret"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

var key = strings.Repeat("k", secret.MinKeyBytes)

func load(t *testing.T, doc string) *Options {
	t.Helper()
	o := &Options{}
	require.NoError(t, yaml.Unmarshal([]byte(doc), o))
	return o
}

func initialized(t *testing.T, doc string) *Options {
	t.Helper()
	o := load(t, doc)
	require.NoError(t, o.Initialize())
	return o
}

func TestDefaults(t *testing.T) {
	o := initialized(t, "{}")
	require.NoError(t, o.Validate())
	require.Empty(t, o.Mode, "the mode is left to the listener: cookie on http, table elsewhere")
	require.Equal(t, DefaultTTL, o.TTLDuration())
	require.Zero(t, o.Idle)
	require.Equal(t, OnUnavailableRepick, o.OnUnavailable)
	require.Equal(t, CookieOptions{
		Name: DefaultCookieName, Path: DefaultCookiePath, Secure: SecureAuto,
		HTTPOnly: o.Cookie.HTTPOnly, SameSite: SameSiteLax, Lifetime: LifetimePermanent,
	}, o.Cookie)
	require.True(t, *o.Cookie.HTTPOnly)
	require.Equal(t, TableOptions{
		Key: "", Learn: LearnRequest, IPv6Prefix: flowkey.DefaultIPv6Prefix,
		MaxEntries: DefaultMaxEntries, KeySource: flowkey.KeySource{Kind: flowkey.KeyClientIP},
	}, o.Table)
	require.Zero(t, o.Header, "only header mode uses a header")
	require.True(t, o.Keys().Ephemeral())
	require.Equal(t, DefaultTTL, (&Options{}).TTLDuration(), "the ttl defaults before Initialize too")

	header := initialized(t, "mode: header\nheader: {name: x-session}\n")
	require.Equal(t, "X-Session", header.Header.Name)
	require.Zero(t, header.Cookie)
	require.Zero(t, header.Table)
	require.Equal(t, DefaultHeaderName, initialized(t, "mode: header\n").Header.Name)

	table := initialized(t, "mode: table\n")
	require.Zero(t, table.Cookie, "table mode issues no cookie")
	require.Equal(t, LearnRequest, table.Table.Learn)
}

func TestLoadsTheDocumentedBlock(t *testing.T) {
	o := initialized(t, `
mode: cookie
ttl: 0
idle: 30m
on_unavailable: reject
secret: `+key+`
cookie:
  name: app_session
  path: /app
  domain: example.com
  secure: true
  http_only: false
  same_site: none
  mark_private: true
`)
	require.NoError(t, o.Validate())
	require.Zero(t, o.TTLDuration(), "an explicit 0 is kept: the cookie lasts the browser session")
	require.Equal(t, 30*time.Minute, time.Duration(o.Idle))
	require.Equal(t, OnUnavailableReject, o.OnUnavailable)
	require.Equal(t, SecureAlways, o.Cookie.Secure, "a YAML true is read as the word")
	require.False(t, *o.Cookie.HTTPOnly)
	require.False(t, o.Keys().Ephemeral())
	require.Empty(t, o.KeyWarning("alb1"))

	table := initialized(t, `
mode: table
table:
  key: header:mcp-session-id
  learn: response
  ipv6_prefix: 56
  max_entries: 500
`)
	require.NoError(t, table.Validate())
	require.Equal(t, flowkey.KeySource{Kind: flowkey.KeyHeader, Name: "Mcp-Session-Id"}, table.Table.KeySource)
	require.Equal(t, 56, table.Table.IPv6Prefix)
	require.Equal(t, 500, table.Table.MaxEntries)
}

func TestValidate(t *testing.T) {
	for doc, want := range map[string]error{
		"mode: sticky\n":                                                ErrInvalidMode,
		"on_unavailable: wait\n":                                        ErrInvalidOnUnavailable,
		"ttl: 500ms\n":                                                  ErrInvalidDuration,
		"ttl: -1s\n":                                                    ErrInvalidDuration,
		"idle: 10ms\n":                                                  ErrInvalidDuration,
		"mode: table\ncookie: {name: x}":                                ErrBlockForOtherMode,
		"mode: header\ncookie: {path: /}":                               ErrBlockForOtherMode,
		"header: {name: X-S}\n":                                         ErrBlockForOtherMode,
		"mode: cookie\nheader: {name: X-S}":                             ErrBlockForOtherMode,
		"mode: cookie\ntable: {key: host}":                              ErrBlockForOtherMode,
		"mode: header\ntable: {max_entries: 5}":                         ErrBlockForOtherMode,
		"cookie: {name: 'bad name'}\n":                                  ErrInvalidCookie,
		"cookie: {path: 'a;b'}\n":                                       ErrInvalidCookie,
		"cookie: {path: app}\n":                                         ErrInvalidCookie,
		"cookie: {domain: 'bad domain'}\n":                              ErrInvalidCookie,
		"cookie: {name: __Secure-s}\n":                                  ErrInvalidCookie,
		"cookie: {name: __Host-s, secure: auto}\n":                      ErrInvalidCookie,
		"cookie: {name: __Host-s, secure: true, path: /app}\n":          ErrInvalidCookie,
		"cookie: {name: __Host-s, secure: true, domain: example.com}\n": ErrInvalidCookie,
		"cookie: {secure: maybe}\n":                                     ErrInvalidSecure,
		"cookie: {same_site: loose}\n":                                  ErrInvalidSameSite,
		"cookie: {lifetime: forever}\n":                                 ErrInvalidLifetime,
		"cookie: {same_site: none, secure: false}\n":                    ErrSameSiteNoneInsecure,
		"cookie: {same_site: none, secure: auto}\n":                     ErrSameSiteNoneInsecure,
		"cookie: {same_site: none}\n":                                   ErrSameSiteNoneInsecure,
		"mode: header\nheader: {name: 'X Session'}\n":                   ErrInvalidHeaderName,
		"table: {key: path}\n":                                          ErrInvalidTableKey,
		"table: {key: method}\n":                                        ErrInvalidTableKey,
		"table: {key: query}\n":                                         ErrInvalidTableKey,
		"table: {key: 'nope:x'}\n":                                      flowkey.ErrInvalidKeySource,
		"table: {learn: always}\n":                                      ErrInvalidLearn,
		"table: {key: client_ip, learn: response}\n":                    ErrLearnResponseKey,
		"table: {key: 'query:s', learn: response}\n":                    ErrLearnResponseKey,
		"table: {ipv6_prefix: 129}\n":                                   ErrInvalidIPv6Prefix,
		"table: {ipv6_prefix: -1}\n":                                    ErrInvalidIPv6Prefix,
		"table: {max_entries: -1}\n":                                    ErrInvalidMaxEntries,
	} {
		// checked without Initialize, which leaves some of these for Validate to report
		require.ErrorIs(t, load(t, doc).Validate(), want, doc)
	}
	for _, doc := range []string{
		"cookie: {name: __Host-s, secure: true, path: /}\n",
		"cookie: {name: __Secure-s, secure: true, path: /app, domain: example.com}\n",
		"cookie: {same_site: none, secure: true}\n",
		"table: {key: 'cookie:s', learn: response}\n",
		"mode: table\ntable: {key: user}\n",
		"ttl: 1s\nidle: 1s\n",
	} {
		o := initialized(t, doc)
		require.NoError(t, o.Validate(), doc)
	}
}

func TestInitializeRefusesAnUnreadableKey(t *testing.T) {
	for doc, want := range map[string]string{
		"secret: short\n":                                              "sticky.secret: " + secret.ErrKeyTooShort.Error(),
		"secret: '${TRICKSTER_STICKY_SECRET}'\n":                       "sticky.secret: " + secret.ErrUnexpandedKey.Error(),
		"secret: " + key + "\nsecret_file: /k\n":                       "sticky.secret: " + secret.ErrKeyAndFile.Error(),
		"secret_file: " + filepath.Join(t.TempDir(), "missing") + "\n": "sticky.secret_file: open ",
		"table: {key: 'nope:x'}\n":                                     "sticky.table.key: ",
	} {
		err := load(t, doc).Initialize()
		require.Error(t, err, doc)
		require.True(t, strings.HasPrefix(err.Error(), want), "%s: %v", doc, err)
		require.NotContains(t, err.Error(), key, "an error does not quote the key")
	}

	file := filepath.Join(t.TempDir(), "sticky.key")
	require.NoError(t, os.WriteFile(file, []byte(key+"\n"), 0o600))
	fromFile := initialized(t, "secret_file: "+file+"\n")
	inline := initialized(t, "secret: "+key+"\n")
	for _, o := range []*Options{fromFile, inline} {
		require.False(t, o.Keys().Ephemeral())
	}
	// both are the same key, so each honors the other's tags
	a, err := fromFile.Keys().Signer("test", secret.MinTagBytes)
	require.NoError(t, err)
	b, err := inline.Keys().Signer("test", secret.MinTagBytes)
	require.NoError(t, err)
	require.True(t, b.Verify(a.Sum(nil, []byte("m")), []byte("m")))
}

func TestKeyWarning(t *testing.T) {
	var none *Options
	require.Empty(t, none.KeyWarning("alb1"))
	require.Empty(t, (&Options{Mode: ModeCookie}).KeyWarning("alb1"), "nothing is read before Initialize")
	for _, mode := range []string{ModeCookie, ModeHeader} {
		w := initialized(t, "mode: "+mode+"\n").KeyWarning("alb1")
		require.Contains(t, w, "alb1")
		require.Contains(t, w, mode+" tokens")
		require.Contains(t, w, "sticky.secret_file")
	}
	require.Empty(t, initialized(t, "mode: table\n").KeyWarning("alb1"), "a table issues no token")
	require.Empty(t, initialized(t, "mode: cookie\nsecret: "+key+"\n").KeyWarning("alb1"))
	// with no mode, an http listener is issued cookies
	w := initialized(t, "{}").KeyWarning("alb1")
	require.Contains(t, w, ModeCookie+" tokens")
	require.Empty(t, initialized(t, "secret: "+key+"\n").KeyWarning("alb1"))
}

func TestModeFor(t *testing.T) {
	for mode, want := range map[string][2]string{
		"":         {ModeCookie, ModeTable},
		ModeCookie: {ModeCookie, ModeCookie},
		ModeHeader: {ModeHeader, ModeHeader},
		ModeTable:  {ModeTable, ModeTable},
	} {
		o := &Options{Mode: mode}
		require.Equal(t, want, [2]string{o.ModeFor(true), o.ModeFor(false)}, "mode %q", mode)
	}
}

func TestClone(t *testing.T) {
	var none *Options
	require.Nil(t, none.Clone())
	o := initialized(t, "secret: "+key+"\n")
	c := o.Clone()
	require.Equal(t, o, c)
	*c.TTL = timeconv.Duration(time.Minute)
	*c.Cookie.HTTPOnly = false
	require.Equal(t, DefaultTTL, o.TTLDuration())
	require.True(t, *o.Cookie.HTTPOnly)
	require.Same(t, o.Keys(), c.Keys(), "a keyring is immutable, so a clone shares it")
	require.Nil(t, (&Options{}).Clone().TTL)
}

func TestSameTable(t *testing.T) {
	a, b := initialized(t, "mode: table\n"), initialized(t, "mode: table\n")
	require.True(t, a.SameTable(b))
	require.False(t, a.SameTable(nil))
	var none *Options
	require.False(t, none.SameTable(a))
	require.False(t, initialized(t, "mode: cookie\n").SameTable(initialized(t, "mode: cookie\n")),
		"cookie mode keeps no table")
	require.False(t, a.SameTable(initialized(t, "mode: table\ntable: {max_entries: 5}\n")))
}

// the key never leaves in what the options marshal to
func TestSecretIsRedacted(t *testing.T) {
	o := initialized(t, "secret: "+key+"\n")
	out, err := yaml.Marshal(o)
	require.NoError(t, err)
	require.NotContains(t, string(out), key)
	require.Contains(t, string(out), "secret: "+secret.Token)
}

func TestSecureCookieName(t *testing.T) {
	require.True(t, SecureCookieName("__Secure-s"))
	require.True(t, SecureCookieName("__Host-s"))
	require.False(t, SecureCookieName("s"))
}
