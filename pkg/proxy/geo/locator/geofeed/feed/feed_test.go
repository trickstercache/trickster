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

package feed

import (
	"net/netip"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"

	"github.com/stretchr/testify/require"
)

const rfcExample = `# IP Prefix,Alpha2code,Region,City,Postal Code
192.0.2.0/25,US,US-AL,,
192.0.2.5,US,US-AL,Alabaster,35007
192.0.2.128/25,PL,PL-MZ,,02-001
2001:db8::/32,PL,,,
2001:db8:cafe::/48,PL,PL-MZ,,02-001
`

func loc(country, sub string) geo.Location {
	l := geo.Location{}
	if country != "" {
		l.Country, _ = geo.ParseCode2(country)
		l.Continent = geo.ContinentOf(l.Country)
	}
	copy(l.Subdivision[:], sub)
	return l
}

func TestParseRFCExample(t *testing.T) {
	// rfcExample is the example feed in RFC 8805, section 2.1.1.1
	var got []Entry
	n, skipped, err := Parse([]byte(rfcExample), func(e Entry) { got = append(got, e) })
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Zero(t, skipped)
	require.Equal(t, []Entry{
		{netip.MustParsePrefix("192.0.2.0/25"), loc("US", "AL")},
		{netip.MustParsePrefix("192.0.2.5/32"), loc("US", "AL")},
		{netip.MustParsePrefix("192.0.2.128/25"), loc("PL", "MZ")},
		{netip.MustParsePrefix("2001:db8::/32"), loc("PL", "")},
		{netip.MustParsePrefix("2001:db8:cafe::/48"), loc("PL", "MZ")},
	}, got)
}

func TestParseLine(t *testing.T) {
	for line, want := range map[string]Entry{
		"203.0.113.0/24,DE,DE-BE":           {netip.MustParsePrefix("203.0.113.0/24"), loc("DE", "BE")},
		" 203.0.113.7/24 , de , de-be , , ": {netip.MustParsePrefix("203.0.113.0/24"), loc("DE", "BE")},
		"203.0.113.0/24":                    {netip.MustParsePrefix("203.0.113.0/24"), geo.Location{}},
		"203.0.113.0/24,,,,":                {netip.MustParsePrefix("203.0.113.0/24"), geo.Location{}},
		"203.0.113.0/24,,US-TX":             {netip.MustParsePrefix("203.0.113.0/24"), loc("US", "TX")},
		"::ffff:203.0.113.0/120,FR":         {netip.MustParsePrefix("203.0.113.0/24"), loc("FR", "")},
		"::ffff:203.0.113.9,FR":             {netip.MustParsePrefix("203.0.113.9/32"), loc("FR", "")},
		"2001:db8::1,JP":                    {netip.MustParsePrefix("2001:db8::1/128"), loc("JP", "")},
	} {
		e, ok, err := ParseLine(line)
		require.NoError(t, err, line)
		require.True(t, ok, line)
		require.Equal(t, want, e, line)
	}
	for _, line := range []string{"", "  ", "# comment", "#203.0.113.0/24,US"} {
		_, ok, err := ParseLine(line)
		require.NoError(t, err, line)
		require.False(t, ok, line)
	}
	for _, line := range []string{
		"203.0.113.0/33,US", "not-an-ip,US", "203.0.113.0/24,UK", "203.0.113.0/24,US,DE-BE",
		"203.0.113.0/24,US,TEXAS", "::ffff:203.0.113.0/64,US", "fe80::1%eth0,US",
	} {
		_, _, err := ParseLine(line)
		require.ErrorIs(t, err, ErrInvalidLine, line)
	}
}

func TestParseSkipsBadLines(t *testing.T) {
	data := []byte("192.0.2.0/24,US\nbad,US\n198.51.100.0/24,UK\n2001:db8::/32,CA")
	var n int
	entries, skipped, err := Parse(data, func(Entry) { n++ })
	require.Equal(t, 2, entries)
	require.Equal(t, 2, n)
	require.Equal(t, 2, skipped)
	require.ErrorIs(t, err, ErrInvalidLine)
	require.Contains(t, err.Error(), "line 2:")
}
