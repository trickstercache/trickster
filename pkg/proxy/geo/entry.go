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
	"fmt"
	"strings"
)

const (
	continentPrefix    = "continent:"
	subdivisionSep     = '-'
	maxSubdivisionLen  = 3
	continentCodesList = "AF, AN, AS, EU, NA, OC, SA"
)

// EntryKind is what a list entry names
type EntryKind uint8

const (
	// EntryCountry names a country, such as US
	EntryCountry EntryKind = iota + 1
	// EntrySubdivision names a country's first-level subdivision, such as US-TX
	EntrySubdivision
	// EntryContinent names a continent, such as continent:EU
	EntryContinent
)

// Entry is one parsed entry of a geo ACL's list
type Entry struct {
	Kind EntryKind
	// Code is the country, or for EntryContinent the continent
	Code Code2
	// Subdivision is set for EntrySubdivision, as in Location
	Subdivision [3]byte
}

// String returns the entry as it is spelled in configuration
func (e Entry) String() string {
	switch e.Kind {
	case EntryContinent:
		return continentPrefix + e.Code.String()
	case EntrySubdivision:
		return e.Code.String() + string(subdivisionSep) + subdivisionString(e.Subdivision)
	}
	return e.Code.String()
}

var (
	// ErrInvalidEntry is wrapped by every error for a list entry that does not parse
	ErrInvalidEntry = errors.New("invalid geo entry")
	// ErrUnassignedCountry is wrapped by the error for a well-formed country code that is not assigned
	ErrUnassignedCountry = errors.New("not an assigned ISO 3166-1 country code")
	// ErrInvalidSubdivision is wrapped by the error for a malformed ISO 3166-2 subdivision code
	ErrInvalidSubdivision = errors.New("not a first-level ISO 3166-2 subdivision code")
)

// ParseEntry parses a list entry, ignoring case and surrounding space: a country (US), a first-level
// subdivision (US-TX) or a continent (continent:EU)
func ParseEntry(s string) (Entry, error) {
	s = strings.TrimSpace(s)
	if len(s) > len(continentPrefix) && strings.EqualFold(s[:len(continentPrefix)], continentPrefix) {
		c, ok := ParseCode2(s[len(continentPrefix):])
		if !ok || !IsContinent(c) {
			return Entry{}, fmt.Errorf("%w %q: a continent is one of %s", ErrInvalidEntry, s, continentCodesList)
		}
		return Entry{Kind: EntryContinent, Code: c}, nil
	}
	if strings.IndexByte(s, subdivisionSep) >= 0 {
		country, sub, err := ParseSubdivision(s)
		if err != nil {
			return Entry{}, fmt.Errorf("%w %q: %w", ErrInvalidEntry, s, err)
		}
		return Entry{Kind: EntrySubdivision, Code: country, Subdivision: sub}, nil
	}
	c, err := ParseCountry(s)
	if err != nil {
		return Entry{}, fmt.Errorf("%w %q: %w", ErrInvalidEntry, s, err)
	}
	return Entry{Kind: EntryCountry, Code: c}, nil
}

// ParseCountry returns the assigned country code s names, ignoring case
func ParseCountry(s string) (Code2, error) {
	c, ok := ParseCode2(s)
	if !ok {
		return Code2{}, fmt.Errorf("%w: a country is two letters", ErrUnassignedCountry)
	}
	if IsCountry(c) {
		return c, nil
	}
	if meant, ok := commonMistakes[c]; ok {
		return Code2{}, fmt.Errorf("%s is %w; use %s", c.String(), ErrUnassignedCountry, meant)
	}
	return Code2{}, fmt.Errorf("%s is %w", c.String(), ErrUnassignedCountry)
}

// ParseSubdivision parses a full first-level ISO 3166-2 code such as US-TX, ignoring case, into its
// assigned country and the part after the hyphen; that part is checked for form only
func ParseSubdivision(s string) (Code2, [3]byte, error) {
	var sub [3]byte
	country, part, ok := strings.Cut(s, string(subdivisionSep))
	if !ok {
		return Code2{}, sub, ErrInvalidSubdivision
	}
	c, err := ParseCountry(country)
	if err != nil {
		return Code2{}, sub, err
	}
	sub, ok = ParseSubdivisionPart(part)
	if !ok {
		return Code2{}, sub, fmt.Errorf("%w: %q is not 1 to 3 letters or digits after the country", ErrInvalidSubdivision, s)
	}
	return c, sub, nil
}

// ParseSubdivisionPart returns the part of a subdivision code after the hyphen, upper-cased, when it is 1 to
// 3 ASCII letters or digits
func ParseSubdivisionPart(s string) ([3]byte, bool) {
	var sub [3]byte
	if len(s) == 0 || len(s) > maxSubdivisionLen {
		return sub, false
	}
	for i := range len(s) {
		b := s[i]
		switch {
		case b >= 'a' && b <= 'z':
			b -= 'a' - 'A'
		case b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		default:
			return [3]byte{}, false
		}
		sub[i] = b
	}
	return sub, true
}

// List is a compiled union of entries; a Match costs O(1) and does not allocate
type List struct {
	countries    [(26*26 + 63) / 64]uint64
	continents   uint8
	subdivisions map[[5]byte]struct{}
	needs        Fields
	n            int
}

// ParseList parses and compiles entries, failing on the first that does not parse
func ParseList(entries []string) (*List, error) {
	l := &List{}
	for _, s := range entries {
		e, err := ParseEntry(s)
		if err != nil {
			return nil, err
		}
		l.Add(e)
	}
	return l, nil
}

// Add adds an entry to the list
func (l *List) Add(e Entry) {
	switch e.Kind {
	case EntryCountry:
		i := e.Code.index()
		l.countries[i>>6] |= 1 << (i & 63)
		l.needs |= FieldCountry
	case EntrySubdivision:
		if l.subdivisions == nil {
			l.subdivisions = make(map[[5]byte]struct{})
		}
		l.subdivisions[subdivisionKey(e.Code, e.Subdivision)] = struct{}{}
		l.needs |= FieldCountry | FieldSubdivision
	case EntryContinent:
		l.continents |= continentBit(e.Code)
		l.needs |= FieldContinent
	default:
		return
	}
	l.n++
}

func subdivisionKey(country Code2, sub [3]byte) [5]byte {
	return [5]byte{country[0], country[1], sub[0], sub[1], sub[2]}
}

// Len returns the number of entries added to the list
func (l *List) Len() int {
	return l.n
}

// Needs returns the location fields the list's entries are matched against
func (l *List) Needs() Fields {
	return l.needs
}

// Match reports whether the location's country, subdivision or continent is listed; a location with no
// continent is matched by its country's
func (l *List) Match(loc Location) bool {
	if loc.Country.isLetters() {
		i := loc.Country.index()
		if l.countries[i>>6]&(1<<(i&63)) != 0 {
			return true
		}
		if l.subdivisions != nil && loc.Subdivision[0] != 0 {
			if _, ok := l.subdivisions[subdivisionKey(loc.Country, loc.Subdivision)]; ok {
				return true
			}
		}
	}
	if l.continents == 0 {
		return false
	}
	if loc.Continent.IsZero() {
		return l.continents&continentBit(ContinentOf(loc.Country)) != 0
	}
	return l.continents&continentBit(loc.Continent) != 0
}
