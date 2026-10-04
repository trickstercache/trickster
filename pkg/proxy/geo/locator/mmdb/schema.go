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

package mmdb

import (
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/mmdb/options"
)

type path []any // map keys and array indexes into a record

type schema struct { // each field's paths are tried in order
	name                            options.Schema
	country, continent, subdivision []path
}

var (
	geoIP2Schema = schema{
		name:        options.SchemaGeoIP2,
		country:     []path{{"country", "iso_code"}},
		continent:   []path{{"continent", "code"}},
		subdivision: []path{{"subdivisions", 0, "iso_code"}},
	}
	ipinfoSchema = schema{
		name: options.SchemaIPinfo,
		// older files keep the codes in country and continent, where Lite files keep names, which are skipped
		country:   []path{{"country_code"}, {"country"}},
		continent: []path{{"continent_code"}, {"continent"}},
	}
)

func customSchema(f *options.Fields) schema {
	s := schema{name: options.SchemaCustom, country: []path{toPath(f.Country)}}
	if len(f.Continent) > 0 {
		s.continent = []path{toPath(f.Continent)}
	}
	if len(f.Subdivision) > 0 {
		s.subdivision = []path{toPath(f.Subdivision)}
	}
	return s
}

func toPath(elements []string) path {
	// an element that is an integer is an array index, and any other a map key
	out := make(path, len(elements))
	for i, e := range elements {
		if n, err := strconv.Atoi(e); err == nil {
			out[i] = n
			continue
		}
		out[i] = e
	}
	return out
}

func candidates(databaseType string) []schema {
	if strings.Contains(strings.ToLower(databaseType), "ipinfo") {
		return []schema{ipinfoSchema, geoIP2Schema}
	}
	return []schema{geoIP2Schema, ipinfoSchema}
}

func parseCountry(s string) (geo.Code2, bool) {
	return geo.ParseCode2(s)
}

func parseContinent(s string) (geo.Code2, bool) {
	c, ok := geo.ParseCode2(s)
	return c, ok && geo.IsContinent(c)
}

func parseSubdivision(s string) ([3]byte, bool) {
	// databases store a subdivision with or without its country and hyphen
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[i+1:]
	}
	return geo.ParseSubdivisionPart(s)
}
