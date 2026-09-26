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

package sticky

import (
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/util/keytable"
)

// Table is where an ALB keeps its pins in table mode: each key's path.
type Table = keytable.Table[Path]

// tables holds each ALB's table and the options it was made under, so that a config reload,
// which rebuilds every ALB, keeps the pins of an ALB whose table would be made the same way
var tables = struct {
	mtx   sync.Mutex
	byALB map[string]keptTable
}{byALB: make(map[string]keptTable)}

type keptTable struct {
	opts  *options.Options
	table *Table
}

// TableFor returns the table the named ALB keeps its pins in, under options that use a table: the
// one it had before a reload when the options that shape a table are unchanged, else a new one.
func TableFor(albName string, o *options.Options) *Table {
	tables.mtx.Lock()
	defer tables.mtx.Unlock()
	if k, ok := tables.byALB[albName]; ok && k.opts.SameTable(o) {
		return k.table
	}
	t := keytable.New[Path](keytable.Options{
		TTL: o.TTLDuration(), Idle: time.Duration(o.Idle), MaxEntries: o.Table.MaxEntries,
	})
	tables.byALB[albName] = keptTable{opts: o.Clone(), table: t}
	return t
}

// ForgetTablesExcept drops the table of every ALB that keep reports false for, such as one the
// running config no longer has.
func ForgetTablesExcept(keep func(albName string) bool) {
	tables.mtx.Lock()
	defer tables.mtx.Unlock()
	for name := range tables.byALB {
		if !keep(name) {
			delete(tables.byALB, name)
		}
	}
}
