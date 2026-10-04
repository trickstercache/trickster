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
	"net/http"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	locatoropts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/options"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const testName = "north-america"

func TestNamedValues(t *testing.T) {
	for name, want := range map[string]Action{"": 0, "reject": ActionReject, " COUNT ": ActionCount} {
		got, err := ParseAction(name)
		require.NoError(t, err, name)
		require.Equal(t, want, got)
	}
	for name, want := range map[string]Verdict{"": 0, "Allow": VerdictAllow, "deny": VerdictDeny} {
		got, err := ParseVerdict(name)
		require.NoError(t, err, name)
		require.Equal(t, want, got)
	}
	_, err := ParseAction("block")
	require.ErrorIs(t, err, ErrInvalidAction)
	require.ErrorContains(t, err, "reject, count")
	_, err = ParseVerdict("maybe")
	require.ErrorIs(t, err, ErrInvalidVerdict)
	require.ErrorContains(t, err, "allow, deny")
	require.Empty(t, Action(0).String())
	require.Empty(t, Verdict(7).String())

	var o Options
	require.NoError(t, yaml.Unmarshal([]byte("action: COUNT\nunknown: Deny\n"), &o))
	require.Equal(t, ActionCount, o.Action)
	require.Equal(t, VerdictDeny, o.Unknown)
	out, err := yaml.Marshal(&o)
	require.NoError(t, err)
	require.Equal(t, "unknown: deny\naction: count\n", string(out))
	out, err = yaml.Marshal(&Options{Deny: []string{"FR"}})
	require.NoError(t, err)
	require.Equal(t, "deny:\n    - FR\n", string(out))
	require.ErrorIs(t, yaml.Unmarshal([]byte("action: block\n"), &o), ErrInvalidAction)
	require.ErrorIs(t, yaml.Unmarshal([]byte("unknown: maybe\n"), &o), ErrInvalidVerdict)
}

func TestValidate(t *testing.T) {
	valid := func() *Options {
		return &Options{Name: testName, Allow: []string{"US", "CA"}}
	}
	require.NoError(t, valid().Validate())
	for _, tc := range []struct {
		edit func(*Options)
		want error
	}{
		{func(o *Options) { o.Name = "" }, ErrInvalidName},
		{func(o *Options) { o.Name = reserved.ReferenceNone }, ErrInvalidName},
		{func(o *Options) { o.Deny = []string{"FR"} }, ErrOneList},
		{func(o *Options) { o.Allow = nil }, ErrOneList},
		{func(o *Options) { o.Allow = []string{"UK"} }, geo.ErrInvalidEntry},
		{func(o *Options) { o.Exempt = []string{"private", "not-an-ip"} }, ErrInvalidExempt},
		{func(o *Options) { o.Message = strings.Repeat("x", MaxMessageLength+1) }, ErrInvalidMessage},
		{func(o *Options) { o.Message = "two\nlines" }, ErrInvalidMessage},
		{func(o *Options) { o.Response = &ResponseOptions{Status: 302} }, ErrInvalidResponse},
		{func(o *Options) { o.Response = &ResponseOptions{Status: 600} }, ErrInvalidResponse},
		{func(o *Options) { o.Response = &ResponseOptions{Headers: map[string]string{"Bad Name": "x"}} },
			ErrInvalidResponse},
		{func(o *Options) { o.Response = &ResponseOptions{Headers: map[string]string{"X-A": "a\nb"}} },
			ErrInvalidResponse},
	} {
		o := valid()
		tc.edit(o)
		require.ErrorIs(t, o.Validate(), tc.want, "%+v", o)
	}
	o := valid()
	o.Message = "Not here."
	o.Exempt = []string{"Private", "198.51.100.0/24", "2001:db8::1"}
	o.Response = &ResponseOptions{Status: 451, Headers: map[string]string{"X-A": "b"}, Body: "{}"}
	require.NoError(t, o.Validate())
}

func TestEffectiveValues(t *testing.T) {
	o := &Options{Name: testName, Deny: []string{"FR"}}
	require.Equal(t, locatoropts.DefaultName, o.LocatorName())
	require.False(t, o.IsAllowList())
	require.Equal(t, []string{"FR"}, o.Entries())
	require.Equal(t, DefaultMessage, o.EffectiveMessage())
	require.Equal(t, http.StatusForbidden, o.EffectiveStatus())
	o.GeoLocatorName = "edge"
	o.Message = "custom"
	o.Response = &ResponseOptions{Status: http.StatusUnavailableForLegalReasons}
	require.Equal(t, "edge", o.LocatorName())
	require.Equal(t, "custom", o.EffectiveMessage())
	require.Equal(t, http.StatusUnavailableForLegalReasons, o.EffectiveStatus())

	private, set, err := ParseExempt([]string{"10.0.0.1", " PRIVATE "})
	require.NoError(t, err)
	require.True(t, private)
	require.Len(t, set, 1)
}

func TestWarnings(t *testing.T) {
	require.Len(t, (&Options{Name: testName, Allow: []string{"US"}}).Warnings(), 1)
	require.Empty(t, (&Options{Name: testName, Allow: []string{"US"}, Exempt: []string{"private"}}).Warnings())
	require.Empty(t, (&Options{Name: testName, Allow: []string{"US"}, Unknown: VerdictDeny}).Warnings())
	w := (&Options{Name: testName, Allow: []string{"US"}, Unknown: VerdictAllow}).Warnings()
	require.Len(t, w, 1)
	require.Contains(t, w[0], "unknown: allow")
	require.Empty(t, (&Options{Name: testName, Deny: []string{"US"}}).Warnings())

	court := &Options{Name: testName, Deny: []string{"FR"}, Response: &ResponseOptions{Status: 451}}
	require.Len(t, court.Warnings(), 1)
	court.Response.Headers = map[string]string{"link": `<https://example.com/x>; REL="blocked-by"`}
	require.Empty(t, court.Warnings())
}

func TestLookup(t *testing.T) {
	l := Lookup{}
	require.NoError(t, yaml.Unmarshal([]byte(`
north-america:
  allow: [US, CA, MX]
  exempt: [private]
  response:
    headers: {X-A: b}
skipped:
`), &l))
	require.NoError(t, l.Validate())
	require.Equal(t, testName, l[testName].Name)
	c := l.Clone()
	require.Equal(t, l[testName], c[testName])
	c[testName].Allow[0] = "FR"
	c[testName].Response.Headers["X-A"] = "c"
	require.Equal(t, "US", l[testName].Allow[0])
	require.Equal(t, "b", l[testName].Response.Headers["X-A"])
	require.Nil(t, Lookup(nil).Clone())
	require.Nil(t, (*Options)(nil).Clone())
	l["bad"] = &Options{Allow: []string{"UK"}}
	require.ErrorIs(t, l.Validate(), geo.ErrInvalidEntry)
}
