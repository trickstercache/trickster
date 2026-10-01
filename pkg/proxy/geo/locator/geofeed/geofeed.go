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
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/filesource"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/feed"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	"github.com/trickstercache/trickster/v2/pkg/watchers/filesystem"
)

// ErrNoFileEntries is returned when replaced files have no entry where the files they replace had some
var ErrNoFileEntries = errors.New("the replaced geofeed files have no entries")

// Locator places addresses by the longest geofeed prefix that holds them
type Locator struct {
	name        string
	inline      []feed.Entry
	table       atomic.Pointer[table]
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
	return l.table.Load().lookup(addr.Unmap()), nil
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
	return l.table.Load().n
}

type v4Level struct {
	mask    uint32
	entries map[uint32]geo.Location
}

type v6Level struct {
	hi, lo  uint64
	entries map[[2]uint64]geo.Location
}

type table struct {
	v4 []v4Level
	v6 []v6Level
	n  int
}

func build(inline, fromFiles []feed.Entry) *table {
	// inline entries are added last, so they beat a file's for the same prefix
	v4 := make(map[int]map[uint32]geo.Location)
	v6 := make(map[int]map[[2]uint64]geo.Location)
	t := &table{}
	add := func(e feed.Entry) {
		bits := e.Prefix.Bits()
		if a := e.Prefix.Addr(); a.Is4() {
			m := v4[bits]
			if m == nil {
				m = make(map[uint32]geo.Location)
				v4[bits] = m
			}
			m[v4Key(a)] = e.Location
			return
		}
		m := v6[bits]
		if m == nil {
			m = make(map[[2]uint64]geo.Location)
			v6[bits] = m
		}
		m[v6Key(e.Prefix.Addr())] = e.Location
	}
	for _, e := range fromFiles {
		add(e)
	}
	for _, e := range inline {
		add(e)
	}
	for bits, m := range v4 {
		t.v4 = append(t.v4, v4Level{mask: v4Mask(bits), entries: m})
		t.n += len(m)
	}
	for bits, m := range v6 {
		hi, lo := v6Mask(bits)
		t.v6 = append(t.v6, v6Level{hi: hi, lo: lo, entries: m})
		t.n += len(m)
	}
	slices.SortFunc(t.v4, func(a, b v4Level) int { return cmp.Compare(b.mask, a.mask) })
	slices.SortFunc(t.v6, func(a, b v6Level) int {
		if c := cmp.Compare(b.hi, a.hi); c != 0 {
			return c
		}
		return cmp.Compare(b.lo, a.lo)
	})
	return t
}

func (t *table) lookup(addr netip.Addr) geo.Location {
	// one map per prefix length present, longest first, so a lookup costs a map read per length
	if addr.Is4() {
		k := v4Key(addr)
		for _, lv := range t.v4 {
			if loc, ok := lv.entries[k&lv.mask]; ok {
				return loc
			}
		}
		return geo.Location{}
	}
	if !addr.IsValid() {
		return geo.Location{}
	}
	k := v6Key(addr)
	for _, lv := range t.v6 {
		if loc, ok := lv.entries[[2]uint64{k[0] & lv.hi, k[1] & lv.lo}]; ok {
			return loc
		}
	}
	return geo.Location{}
}

func v4Key(a netip.Addr) uint32 {
	b := a.As4()
	return binary.BigEndian.Uint32(b[:])
}

func v6Key(a netip.Addr) [2]uint64 {
	b := a.As16()
	return [2]uint64{binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])}
}

func v4Mask(bits int) uint32 {
	if bits == 0 {
		return 0
	}
	return ^uint32(0) << (32 - bits)
}

func v6Mask(bits int) (hi, lo uint64) {
	switch {
	case bits == 0:
		return 0, 0
	case bits <= 64:
		return ^uint64(0) << (64 - bits), 0
	case bits == 128:
		return ^uint64(0), ^uint64(0)
	}
	return ^uint64(0), ^uint64(0) << (128 - bits)
}
