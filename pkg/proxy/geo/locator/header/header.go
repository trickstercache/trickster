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

// Package header places HTTP requests by a location header that a trusted CDN or load balancer set.
package header

import (
	"net/http"
	"net/netip"
	"strings"

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/header/options"
)

// Locator reads a request's location from its headers, believing them only from a trusted proxy
type Locator struct {
	country, subdivision, continent string
	unknown                         []string
	serves                          geo.Fields
}

// New returns a Locator for the options
func New(o *options.Options) *Locator {
	l := &Locator{
		country:     http.CanonicalHeaderKey(o.Country),
		subdivision: http.CanonicalHeaderKey(o.Subdivision),
		continent:   http.CanonicalHeaderKey(o.Continent),
		unknown:     o.UnknownValues,
		// a country's continent is known without a continent header
		serves: geo.FieldCountry | geo.FieldContinent,
	}
	if l.subdivision != "" {
		l.serves |= geo.FieldSubdivision
	}
	return l
}

// LocateRequest reads the request's location headers when its peer is one of the listener's trusted proxies;
// from any other peer it reads nothing, and the request has no location
func (l *Locator) LocateRequest(r *http.Request) (geo.Location, error) {
	var loc geo.Location
	if !tctx.PeerTrusted(r.Context()) {
		return loc, nil
	}
	country, ok := geo.ParseCode2(l.value(r.Header, l.country))
	if !ok {
		return loc, nil
	}
	loc.Country = country
	if l.subdivision != "" {
		if v := l.value(r.Header, l.subdivision); v != "" {
			// a subdivision may come with its country, which must then be the request's
			if c, part, found := strings.Cut(v, "-"); found {
				if code, ok := geo.ParseCode2(c); ok && code == country {
					v = part
				} else {
					v = ""
				}
			}
			if sub, ok := geo.ParseSubdivisionPart(v); ok {
				loc.Subdivision = sub
			}
		}
	}
	if l.continent != "" {
		if c, ok := geo.ParseCode2(l.value(r.Header, l.continent)); ok && geo.IsContinent(c) {
			loc.Continent = c
		}
	}
	if loc.Continent.IsZero() {
		loc.Continent = geo.ContinentOf(country)
	}
	return loc, nil
}

func (l *Locator) value(h http.Header, name string) string {
	// an absent header and one of the unknown values are both no value
	v := h[name]
	if len(v) == 0 {
		return ""
	}
	s := strings.TrimSpace(v[0])
	for _, u := range l.unknown {
		if strings.EqualFold(s, u) {
			return ""
		}
	}
	return s
}

// Locate has no answer, since the location is in a request's headers, not its address
func (l *Locator) Locate(netip.Addr) (geo.Location, error) {
	return geo.Location{}, nil
}

// Serves reports country and continent, and subdivision when a subdivision header is configured
func (l *Locator) Serves() geo.Fields {
	return l.serves
}

// Close does nothing; the locator holds nothing open
func (l *Locator) Close() error {
	return nil
}
