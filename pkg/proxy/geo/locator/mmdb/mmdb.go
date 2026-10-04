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

// Package mmdb places client addresses with a MaxMind DB format file, held in memory and swapped for its
// replacement while serving.
package mmdb

import (
	"errors"
	"fmt"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/filesource"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/mmdb/options"
	"github.com/trickstercache/trickster/v2/pkg/watchers/filesystem"

	"github.com/oschwald/maxminddb-golang/v2"
)

const sampleRecords = 256 // records a load reads to choose a schema and learn the fields a file serves

var probes = [...]netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")}

var (
	// ErrNoSchema is returned for a file whose records no schema can read a country from
	ErrNoSchema = errors.New("no record places a country by the schema; set 'schema: custom' and its 'fields'")
	// ErrFieldsLost is returned for a replacement that serves fewer location fields than the file it replaces
	ErrFieldsLost = errors.New("the replacement serves fewer location fields than the file it replaces")
)

type database struct {
	reader   *maxminddb.Reader
	schema   schema
	serves   geo.Fields
	ipv4Only bool
	cache    recordCache
}

// Locator places addresses with the file it has loaded; it is safe for concurrent use
type Locator struct {
	name    string
	opts    *options.Options
	db      atomic.Pointer[database]
	watcher *filesystem.Watcher
	serves  geo.Fields // fixed by the first load, so a replacement can never take a field an ACL needs
}

// New returns a Locator for the options, loading its file before it returns
func New(name string, o *options.Options) (*Locator, error) {
	l := &Locator{name: name, opts: o}
	w, err := filesource.Watch(name, []string{o.Path}, time.Duration(o.ReloadInterval), l.load)
	if err != nil {
		return nil, fmt.Errorf("mmdb %q: %w", o.Path, err)
	}
	l.watcher = w
	return l, nil
}

func (l *Locator) load(contents [][]byte) error {
	d, err := open(contents[0], l.opts)
	if err != nil {
		return err
	}
	if l.db.Load() == nil {
		l.serves = d.serves
	} else if !d.serves.Has(l.serves) {
		return fmt.Errorf("%w: %s, not %s", ErrFieldsLost, d.serves, l.serves)
	}
	// the replaced reader is never closed, since a lookup may still be reading it; it is freed once none is
	l.db.Store(d)
	built := d.reader.Metadata.BuildTime()
	logger.Info("mmdb loaded", logging.Pairs{
		keys.GeoLocator: l.name, keys.Type: d.reader.Metadata.DatabaseType, keys.Fields: d.serves.String(),
		keys.Built: built.UTC().Format(time.RFC3339),
	})
	if d.ipv4Only {
		logger.Warn("mmdb holds IPv4 networks only, so no IPv6 client has a location",
			logging.Pairs{keys.GeoLocator: l.name})
	}
	if maxAge := time.Duration(l.opts.MaxAge); maxAge > 0 && time.Since(built) > maxAge {
		logger.Warn("mmdb was built longer ago than max_age", logging.Pairs{
			keys.GeoLocator: l.name, keys.Built: built.UTC().Format(time.RFC3339), keys.MaxAge: maxAge.String(),
		})
	}
	return nil
}

func open(data []byte, o *options.Options) (*database, error) {
	// records are read only through the reader's path decoding, which bounds its work on a hostile file
	r, err := maxminddb.OpenBytes(data)
	if err != nil {
		return nil, err
	}
	d := &database{reader: r, ipv4Only: r.Metadata.IPVersion == 4}
	// a lookup of a documentation address must succeed or miss without an error
	for _, a := range probes {
		if a.Is6() && d.ipv4Only {
			continue
		}
		if err := r.Lookup(a).Err(); err != nil {
			return nil, err
		}
	}
	var schemas []schema
	switch o.Schema {
	case options.SchemaGeoIP2:
		schemas = []schema{geoIP2Schema}
	case options.SchemaIPinfo:
		schemas = []schema{ipinfoSchema}
	case options.SchemaCustom:
		schemas = []schema{customSchema(o.Fields)}
	default:
		schemas = candidates(r.Metadata.DatabaseType)
	}
	seen, err := sample(r, schemas)
	if err != nil {
		return nil, err
	}
	for i, s := range schemas {
		if !seen[i].Has(geo.FieldCountry) {
			continue
		}
		d.schema = s
		d.serves = geo.FieldCountry | seen[i]&geo.FieldContinent
		if len(s.subdivision) == 0 {
			return d, nil
		}
		found := seen[i].Has(geo.FieldSubdivision)
		if !found {
			if found, err = placesSubdivision(r, &d.schema); err != nil {
				return nil, err
			}
		}
		if found {
			d.serves |= geo.FieldSubdivision
		}
		return d, nil
	}
	return nil, ErrNoSchema
}

func placesSubdivision(r *maxminddb.Reader, s *schema) (bool, error) {
	// a file may place its first networks by country alone, so only a record with a subdivision proves the field
	for res := range r.Networks() {
		if err := res.Err(); err != nil {
			return false, err
		}
		if !res.Found() {
			continue
		}
		v, err := firstString(res, s.subdivision)
		if err != nil {
			return false, err
		}
		if _, ok := parseSubdivision(v); !ok {
			continue
		}
		loc, err := decode(res, s)
		if err != nil {
			return false, err
		}
		if loc.Subdivision[0] != 0 {
			return true, nil
		}
	}
	return false, nil
}

func sample(r *maxminddb.Reader, schemas []schema) ([]geo.Fields, error) {
	seen := make([]geo.Fields, len(schemas))
	var n int
	for res := range r.Networks() {
		if err := res.Err(); err != nil {
			return nil, err
		}
		if !res.Found() {
			continue
		}
		for i := range schemas {
			loc, err := decode(res, &schemas[i])
			if err != nil {
				return nil, err
			}
			seen[i] |= fieldsOf(loc)
		}
		if n++; n >= sampleRecords {
			break
		}
	}
	return seen, nil
}

func fieldsOf(loc geo.Location) geo.Fields {
	var f geo.Fields
	if !loc.Country.IsZero() {
		f |= geo.FieldCountry
	}
	if !loc.Continent.IsZero() {
		f |= geo.FieldContinent
	}
	if loc.Subdivision[0] != 0 {
		f |= geo.FieldSubdivision
	}
	return f
}

// Locate returns where the file places addr. An address in no network of the file has no location.
func (l *Locator) Locate(addr netip.Addr) (geo.Location, error) {
	d := l.db.Load()
	if d.ipv4Only && !addr.Is4() {
		return geo.Location{}, nil
	}
	res := d.reader.Lookup(addr)
	if err := res.Err(); err != nil {
		return geo.Location{}, err
	}
	if !res.Found() {
		return geo.Location{}, nil
	}
	offset := res.Offset()
	if loc, ok := d.cache.get(offset); ok {
		return loc, nil
	}
	loc, err := decode(res, &d.schema)
	if err != nil {
		return geo.Location{}, err
	}
	d.cache.put(offset, loc)
	return loc, nil
}

func decode(res maxminddb.Result, s *schema) (geo.Location, error) {
	var loc geo.Location
	v, err := firstString(res, s.country)
	if err != nil {
		return loc, err
	}
	if c, ok := parseCountry(v); ok {
		loc.Country = c
	}
	if v, err = firstString(res, s.continent); err != nil {
		return loc, err
	}
	if c, ok := parseContinent(v); ok {
		loc.Continent = c
	}
	if loc.Country.IsZero() || len(s.subdivision) == 0 {
		return loc, nil
	}
	if v, err = firstString(res, s.subdivision); err != nil {
		return loc, err
	}
	if sub, ok := parseSubdivision(v); ok {
		loc.Subdivision = sub
	}
	return loc, nil
}

func firstString(res maxminddb.Result, paths []path) (string, error) {
	// a path to no string is skipped; only a damaged record is an error
	for _, p := range paths {
		var s *string
		if err := res.DecodePath(&s, p...); err != nil {
			if _, ok := errors.AsType[maxminddb.InvalidDatabaseError](err); ok {
				return "", err
			}
			continue
		}
		if s != nil && *s != "" {
			return *s, nil
		}
	}
	return "", nil
}

// Serves reports the location fields the first loaded file serves, which every replacement must too
func (l *Locator) Serves() geo.Fields {
	return l.serves
}

// BuildTime returns when the loaded file was built
func (l *Locator) BuildTime() time.Time {
	return l.db.Load().reader.Metadata.BuildTime()
}

// Close stops watching the file. The loaded file stays readable, since a lookup may still be underway.
func (l *Locator) Close() error {
	l.watcher.Close()
	return nil
}
