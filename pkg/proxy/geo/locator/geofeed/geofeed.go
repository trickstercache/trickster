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

// Package geofeed places client addresses by RFC 8805 geofeed entries, given inline or in watched files.
package geofeed

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
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/feed"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	"github.com/trickstercache/trickster/v2/pkg/util/prefixtable"
	"github.com/trickstercache/trickster/v2/pkg/watchers/filesystem"
)

// ErrNoFileEntries is returned when replaced files have no entry where the files they replace had some
var ErrNoFileEntries = errors.New("the replaced geofeed files have no entries")

// Locator places addresses by the longest geofeed prefix that holds them
type Locator struct {
	name        string
	inline      []feed.Entry
	table       atomic.Pointer[prefixtable.Table[geo.Location]]
	watcher     *filesystem.Watcher
	fileEntries int // from files, in the current table; only the watcher's loads touch it
}

// New returns a Locator for the options, loading its files before it returns
func New(name string, o *options.Options) (*Locator, error) {
	l := &Locator{name: name}
	for i, line := range o.Entries {
		e, ok, err := feed.ParseLine(line)
		if err != nil {
			return nil, fmt.Errorf("geofeed 'entries' item %d: %w", i, err)
		}
		if ok {
			l.inline = append(l.inline, e)
		}
	}
	if len(o.Files) == 0 {
		l.table.Store(build(l.inline, nil))
		return l, nil
	}
	w, err := filesource.Watch(name, o.Files, time.Duration(o.ReloadInterval), l.load)
	if err != nil {
		return nil, fmt.Errorf("geofeed 'files': %w", err)
	}
	l.watcher = w
	return l, nil
}

func (l *Locator) load(contents [][]byte) error {
	var fromFiles []feed.Entry
	var skipped int
	var firstErr error
	for _, data := range contents {
		_, s, err := feed.Parse(data, func(e feed.Entry) { fromFiles = append(fromFiles, e) })
		skipped += s
		if firstErr == nil {
			firstErr = err
		}
	}
	if len(fromFiles) == 0 && l.fileEntries > 0 {
		return ErrNoFileEntries
	}
	if skipped > 0 {
		logger.Warn("geofeed lines skipped", logging.Pairs{
			keys.GeoLocator: l.name, keys.Size: skipped, keys.Detail: firstErr.Error(),
		})
	}
	l.table.Store(build(l.inline, fromFiles))
	l.fileEntries = len(fromFiles)
	logger.Info("geofeed loaded", logging.Pairs{keys.GeoLocator: l.name, keys.Size: len(fromFiles) + len(l.inline)})
	return nil
}

// Locate returns the location of the longest prefix holding addr
func (l *Locator) Locate(addr netip.Addr) (geo.Location, error) {
	loc, _ := l.table.Load().Lookup(addr.Unmap())
	return loc, nil
}

// Serves reports every location field, since a geofeed line can name a region and its country's continent is known
func (l *Locator) Serves() geo.Fields {
	return geo.FieldsAll
}

// Close stops watching the locator's files
func (l *Locator) Close() error {
	if l.watcher != nil {
		l.watcher.Close()
	}
	return nil
}

// Len returns the number of distinct prefixes the locator holds
func (l *Locator) Len() int {
	return l.table.Load().Len()
}

func build(inline, fromFiles []feed.Entry) *prefixtable.Table[geo.Location] {
	// inline entries are set last, so they beat a file's for the same prefix
	var b prefixtable.Builder[geo.Location]
	for _, e := range fromFiles {
		b.Set(e.Prefix, e.Location)
	}
	for _, e := range inline {
		b.Set(e.Prefix, e.Location)
	}
	t := b.Table()
	return &t
}
