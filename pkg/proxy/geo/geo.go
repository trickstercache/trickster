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

// Package geo provides client locations, the country and continent codes that describe
// them, and the compiled lists of location entries that geo ACLs judge them by.
package geo

// Code2 is an upper-case two-letter code: an ISO 3166-1 alpha-2 country, or a continent
type Code2 [2]byte

// IsZero reports whether the code is unset
func (c Code2) IsZero() bool {
	return c == Code2{}
}

// String returns the code, or an empty string when it is unset
func (c Code2) String() string {
	if c.IsZero() {
		return ""
	}
	return string(c[:])
}

// Location is where a locator placed a client; its zero value is no location
type Location struct {
	// Country is an ISO 3166-1 alpha-2 code
	Country Code2
	// Continent is one of AF AN AS EU NA OC SA
	Continent Code2
	// Subdivision is the part of a first-level ISO 3166-2 code after the hyphen, left-aligned and
	// zero-padded; it is meaningful only with Country
	Subdivision [3]byte
}

// IsZero reports whether the location is no location
func (l Location) IsZero() bool {
	return l == Location{}
}

// String returns the location as country, country-subdivision or continent:code, for logs
func (l Location) String() string {
	switch {
	case !l.Country.IsZero() && l.Subdivision[0] != 0:
		return l.Country.String() + "-" + subdivisionString(l.Subdivision)
	case !l.Country.IsZero():
		return l.Country.String()
	case !l.Continent.IsZero():
		return continentPrefix + l.Continent.String()
	}
	return ""
}

func subdivisionString(s [3]byte) string {
	n := 0
	for n < len(s) && s[n] != 0 {
		n++
	}
	return string(s[:n])
}

// Fields is a set of the location fields a locator can fill
type Fields uint8

const (
	// FieldCountry is Location.Country
	FieldCountry Fields = 1 << iota
	// FieldContinent is Location.Continent
	FieldContinent
	// FieldSubdivision is Location.Subdivision
	FieldSubdivision
)

// FieldsAll is every location field
const FieldsAll = FieldCountry | FieldContinent | FieldSubdivision

// Has reports whether f holds every field in g
func (f Fields) Has(g Fields) bool {
	return f&g == g
}

// String returns the names of the fields in f, separated by commas
func (f Fields) String() string {
	var out string
	for _, n := range []struct {
		f    Fields
		name string
	}{{FieldCountry, "country"}, {FieldContinent, "continent"}, {FieldSubdivision, "subdivision"}} {
		if f&n.f == 0 {
			continue
		}
		if out != "" {
			out += ", "
		}
		out += n.name
	}
	return out
}
