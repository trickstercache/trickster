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
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/util/keytable"

	"github.com/prometheus/client_golang/prometheus"
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

// ForgetTablesExcept drops every kept table that keep reports false for. A config build calls it
// once its ALBs are running, keeping only the tables they hold, so a table no ALB uses is not
// carried into a later config that asks for one again.
func ForgetTablesExcept(keep func(albName string, t *Table) bool) {
	tables.mtx.Lock()
	defer tables.mtx.Unlock()
	for name, k := range tables.byALB {
		if !keep(name, k.table) {
			delete(tables.byALB, name)
		}
	}
}

// entriesDesc describes the table gauge. It is filled at scrape time from the kept tables, so a
// pin costs nothing for it and a table dropped at reload takes its series with it.
var entriesDesc = prometheus.NewDesc(
	"trickster_alb_sticky_entries",
	"Current number of pins an ALB keeps in its sticky table, expired ones not yet removed included.",
	[]string{keys.ALB_Name}, nil,
)

type entriesCollector struct{}

func (entriesCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- entriesDesc
}

func (entriesCollector) Collect(ch chan<- prometheus.Metric) {
	tables.mtx.Lock()
	kept := make(map[string]*Table, len(tables.byALB))
	for name, k := range tables.byALB {
		kept[name] = k.table
	}
	tables.mtx.Unlock()
	for name, t := range kept {
		ch <- prometheus.MustNewConstMetric(entriesDesc, prometheus.GaugeValue, float64(t.Len()), name)
	}
}

func init() {
	prometheus.MustRegister(entriesCollector{})
}
