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

// Package feed parses RFC 8805 geofeeds: lines of prefix, country, region, city and postal code.
package feed

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
)

const (
	fieldSep         = ','
	commentMark      = '#'
	mappedPrefixBits = 96 // how much longer an IPv4-mapped IPv6 prefix is than the IPv4 prefix it maps
)

// ErrInvalidLine is wrapped by every error for a line that does not parse
var ErrInvalidLine = errors.New("invalid geofeed line")

// Entry is one geofeed line: a masked prefix, and where it is. A zero Location says the prefix has no location.
type Entry struct {
	Prefix   netip.Prefix
	Location geo.Location
}

// ParseLine parses one geofeed line, reporting false for a blank or comment line. City and postal code are
// ignored; a region is a full ISO 3166-2 code of the line's country.
func ParseLine(line string) (Entry, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == commentMark {
		return Entry{}, false, nil
	}
	prefixField, rest, _ := strings.Cut(line, string(fieldSep))
	countryField, rest, _ := strings.Cut(rest, string(fieldSep))
	regionField, _, _ := strings.Cut(rest, string(fieldSep))
	prefix, err := parsePrefix(strings.TrimSpace(prefixField))
	if err != nil {
		return Entry{}, false, fmt.Errorf("%w %q: %w", ErrInvalidLine, line, err)
	}
	e := Entry{Prefix: prefix}
	if s := strings.TrimSpace(countryField); s != "" {
		if e.Location.Country, err = geo.ParseCountry(s); err != nil {
			return Entry{}, false, fmt.Errorf("%w %q: %w", ErrInvalidLine, line, err)
		}
	}
	if s := strings.TrimSpace(regionField); s != "" {
		country, sub, err := geo.ParseSubdivision(s)
		if err != nil {
			return Entry{}, false, fmt.Errorf("%w %q: %w", ErrInvalidLine, line, err)
		}
		if !e.Location.Country.IsZero() && country != e.Location.Country {
			return Entry{}, false, fmt.Errorf("%w %q: the region's country is not the line's", ErrInvalidLine, line)
		}
		e.Location.Country, e.Location.Subdivision = country, sub
	}
	e.Location.Continent = geo.ContinentOf(e.Location.Country)
	return e, true, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	var p netip.Prefix
	if strings.IndexByte(s, '/') >= 0 {
		var err error
		if p, err = netip.ParsePrefix(s); err != nil {
			return netip.Prefix{}, err
		}
	} else {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		if a.Zone() != "" {
			return netip.Prefix{}, errors.New("an address may not have a zone")
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if a := p.Addr(); a.Is4In6() {
		if p.Bits() < mappedPrefixBits {
			return netip.Prefix{}, errors.New("an IPv4-mapped prefix must be at least /96")
		}
		p = netip.PrefixFrom(a.Unmap(), p.Bits()-mappedPrefixBits)
	}
	return p.Masked(), nil
}

// Parse calls fn with each entry of a geofeed, skipping lines that do not parse; it returns the counts of
// entries and skipped lines, and the first skipped line's error
func Parse(data []byte, fn func(Entry)) (entries, skipped int, firstErr error) {
	for lineNum := 1; len(data) > 0; lineNum++ {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		e, ok, err := ParseLine(string(line))
		if err != nil {
			skipped++
			if firstErr == nil {
				firstErr = fmt.Errorf("line %d: %w", lineNum, err)
			}
			continue
		}
		if ok {
			entries++
			fn(e)
		}
	}
	return entries, skipped, firstErr
}
