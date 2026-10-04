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

package geo

import (
	"errors"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

const assignedCountryCount = 250 // the 249 assigned ISO 3166-1 codes, and XK

func code(s string) Code2 {
	return Code2{s[0], s[1]}
}

func sub(s string) [3]byte {
	var out [3]byte
	copy(out[:], s)
	return out
}

func TestAssignedCountries(t *testing.T) {
	var n int
	for a := byte('A'); a <= 'Z'; a++ {
		for b := byte('A'); b <= 'Z'; b++ {
			c := Code2{a, b}
			if !IsCountry(c) {
				require.True(t, ContinentOf(c).IsZero(), c.String())
				continue
			}
			n++
			require.True(t, IsContinent(ContinentOf(c)), c.String())
		}
	}
	require.Equal(t, assignedCountryCount, n)
	for country, continent := range map[string]string{
		"US": "NA", "GB": "EU", "XK": "EU", "AQ": "AN", "AU": "OC", "BR": "SA", "ZA": "AF", "JP": "AS",
	} {
		require.Equal(t, code(continent), ContinentOf(code(country)), country)
	}
	require.True(t, ContinentOf(Code2{}).IsZero())
	require.True(t, ContinentOf(Code2{'a', '1'}).IsZero())
	require.False(t, IsCountry(Code2{}))
	require.False(t, IsContinent(Code2{}))
	require.False(t, IsContinent(code("US")))
}

func TestParseCode2(t *testing.T) {
	for in, want := range map[string]Code2{"us": code("US"), "Us": code("US"), "ZZ": code("ZZ")} {
		got, ok := ParseCode2(in)
		require.True(t, ok, in)
		require.Equal(t, want, got)
	}
	for _, in := range []string{"", "U", "USA", "U1", "U-", "é"} {
		_, ok := ParseCode2(in)
		require.False(t, ok, in)
	}
}

func TestParseEntry(t *testing.T) {
	for in, want := range map[string]Entry{
		"US":           {Kind: EntryCountry, Code: code("US")},
		" us ":         {Kind: EntryCountry, Code: code("US")},
		"XK":           {Kind: EntryCountry, Code: code("XK")},
		"US-TX":        {Kind: EntrySubdivision, Code: code("US"), Subdivision: sub("TX")},
		"fr-75":        {Kind: EntrySubdivision, Code: code("FR"), Subdivision: sub("75")},
		"GB-ENG":       {Kind: EntrySubdivision, Code: code("GB"), Subdivision: sub("ENG")},
		"ca-q":         {Kind: EntrySubdivision, Code: code("CA"), Subdivision: sub("Q")},
		"continent:EU": {Kind: EntryContinent, Code: code("EU")},
		"Continent:na": {Kind: EntryContinent, Code: code("NA")},
	} {
		got, err := ParseEntry(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
		again, err := ParseEntry(got.String())
		require.NoError(t, err, got.String())
		require.Equal(t, got, again)
	}
	for in, wantSuffix := range map[string]string{
		"UK":           "use GB",
		"EL":           "use GR",
		"EU":           "use continent:EU",
		"ZZ":           ErrUnassignedCountry.Error(),
		"USA":          "a country is two letters",
		"":             "a country is two letters",
		"U1":           "a country is two letters",
		"continent:ZZ": continentCodesList,
		"continent:":   "a country is two letters",
		"US-":          "after the country",
		"US-TEXA":      "after the country",
		"US-T_":        "after the country",
		"ZZ-TX":        ErrUnassignedCountry.Error(),
		"UK-ENG":       "use GB",
	} {
		_, err := ParseEntry(in)
		require.ErrorIs(t, err, ErrInvalidEntry, in)
		require.True(t, strings.HasSuffix(err.Error(), wantSuffix), "%q: %v", in, err)
	}
	_, _, err := ParseSubdivision("US")
	require.ErrorIs(t, err, ErrInvalidSubdivision)
	require.Equal(t, "", Entry{}.String())
}

func TestLocationString(t *testing.T) {
	for want, loc := range map[string]Location{
		"":             {},
		"US":           {Country: code("US"), Continent: code("NA")},
		"US-TX":        {Country: code("US"), Continent: code("NA"), Subdivision: sub("TX")},
		"continent:EU": {Continent: code("EU")},
	} {
		require.Equal(t, want, loc.String())
		require.Equal(t, want == "", loc.IsZero())
	}
}

func TestFields(t *testing.T) {
	require.Equal(t, "country, continent, subdivision", FieldsAll.String())
	require.Equal(t, "continent", FieldContinent.String())
	require.Equal(t, "", Fields(0).String())
	require.True(t, FieldsAll.Has(FieldCountry|FieldSubdivision))
	require.False(t, FieldCountry.Has(FieldCountry|FieldSubdivision))
}

func TestListMatch(t *testing.T) {
	l, err := ParseList([]string{"US", "CA-QC", "continent:OC"})
	require.NoError(t, err)
	require.Equal(t, 3, l.Len())
	require.Equal(t, FieldsAll, l.Needs())
	for _, tc := range []struct {
		loc  Location
		want bool
	}{
		{Location{Country: code("US"), Continent: code("NA")}, true},
		{Location{Country: code("US"), Subdivision: sub("TX")}, true},
		{Location{Country: code("CA"), Continent: code("NA"), Subdivision: sub("QC")}, true},
		{Location{Country: code("CA"), Continent: code("NA"), Subdivision: sub("ON")}, false},
		{Location{Country: code("CA"), Continent: code("NA")}, false},
		{Location{Country: code("AU"), Continent: code("OC")}, true},
		{Location{Country: code("NZ")}, true}, // no continent: the country's is used
		{Location{Continent: code("OC")}, true},
		{Location{Continent: code("EU")}, false},
		{Location{Country: code("DE"), Continent: code("EU")}, false},
		{Location{}, false},
	} {
		require.Equal(t, tc.want, l.Match(tc.loc), tc.loc.String())
	}

	_, err = ParseList([]string{"US", "UK"})
	require.ErrorIs(t, err, ErrInvalidEntry)

	empty := &List{}
	empty.Add(Entry{})
	require.Zero(t, empty.Len())
	require.False(t, empty.Match(Location{Country: code("US"), Continent: code("NA")}))
}

func naiveMatch(entries []Entry, loc Location) bool {
	continent := loc.Continent
	if continent.IsZero() {
		continent = ContinentOf(loc.Country)
	}
	for _, e := range entries {
		switch e.Kind {
		case EntryCountry:
			if e.Code == loc.Country {
				return true
			}
		case EntrySubdivision:
			if e.Code == loc.Country && e.Subdivision == loc.Subdivision {
				return true
			}
		case EntryContinent:
			if e.Code == continent {
				return true
			}
		}
	}
	return false
}

func TestListMatchProperty(t *testing.T) {
	var countries []Code2
	for a := byte('A'); a <= 'Z'; a++ {
		for b := byte('A'); b <= 'Z'; b++ {
			if c := (Code2{a, b}); IsCountry(c) {
				countries = append(countries, c)
			}
		}
	}
	subs := [][3]byte{sub("TX"), sub("CA"), sub("75"), sub("ENG"), {}}
	rng := weaktest.NewRand(8805, 3166)
	randomLocation := func() Location {
		var loc Location
		if rng.IntN(8) != 0 {
			loc.Country = countries[rng.IntN(len(countries))]
			loc.Subdivision = subs[rng.IntN(len(subs))]
		}
		if rng.IntN(4) != 0 {
			loc.Continent = continents[rng.IntN(len(continents))]
		}
		return loc
	}
	for range 500 {
		entries := make([]Entry, rng.IntN(12))
		l := &List{}
		for i := range entries {
			switch rng.IntN(3) {
			case 0:
				entries[i] = Entry{Kind: EntryCountry, Code: countries[rng.IntN(len(countries))]}
			case 1:
				entries[i] = Entry{Kind: EntrySubdivision, Code: countries[rng.IntN(len(countries))],
					Subdivision: subs[rng.IntN(len(subs)-1)]}
			default:
				entries[i] = Entry{Kind: EntryContinent, Code: continents[rng.IntN(len(continents))]}
			}
			l.Add(entries[i])
		}
		for range 50 {
			loc := randomLocation()
			if loc.Country.IsZero() {
				loc.Subdivision = [3]byte{}
			}
			require.Equal(t, naiveMatch(entries, loc), l.Match(loc), "%v %v", entries, loc)
		}
	}
}

func TestErrorsWrap(t *testing.T) {
	_, err := ParseCountry("UK")
	require.True(t, errors.Is(err, ErrUnassignedCountry))
}

func BenchmarkListMatch(b *testing.B) {
	l, err := ParseList([]string{"US", "CA", "MX", "US-TX", "continent:EU"})
	require.NoError(b, err)
	locs := []Location{
		{Country: code("US"), Continent: code("NA"), Subdivision: sub("TX")},
		{Country: code("DE"), Continent: code("EU")},
		{Country: code("JP"), Continent: code("AS"), Subdivision: sub("13")},
	}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		l.Match(locs[i%len(locs)])
	}
}
