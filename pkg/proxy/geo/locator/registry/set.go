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

package registry

import (
	"fmt"
	"maps"
	"slices"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/options"

	"github.com/prometheus/client_golang/prometheus"
)

// Running is a built locator and the options it was built from
type Running struct {
	Options *options.Options
	Locator locator.Locator
}

// Set is the running locators of a configuration, by name
type Set map[string]*Running

// Build returns a Set of the named locators, keeping previous's unchanged ones and building the rest; on
// an error it closes what it built
func Build(names []string, lookup options.Lookup, previous Set) (Set, error) {
	next := make(Set, len(names))
	for _, name := range names {
		o := lookup[name]
		if o == nil {
			next.CloseExcept(previous)
			return nil, fmt.Errorf("geo locator %q is not defined", name)
		}
		if r := previous[name]; r != nil && r.Options.Equal(o) {
			next[name] = r
			continue
		}
		l, err := New(o)
		if err != nil {
			next.CloseExcept(previous)
			return nil, err
		}
		next[name] = &Running{Options: o, Locator: l}
	}
	return next, nil
}

// CloseExcept closes the locators of s that other does not hold: a replaced Set's once its successor is
// serving, or a failed apply's new Set's against the one still serving
func (s Set) CloseExcept(other Set) {
	for name, r := range s {
		if o := other[name]; o == nil || o.Locator != r.Locator {
			closeLocator(name, r.Locator)
		}
	}
}

// Close closes every locator of s
func (s Set) Close() {
	for _, name := range slices.Sorted(maps.Keys(s)) {
		closeLocator(name, s[name].Locator)
	}
}

// Locator returns the named running locator, or nil
func (s Set) Locator(name string) locator.Locator {
	if r := s[name]; r != nil {
		return r.Locator
	}
	return nil
}

func closeLocator(name string, l locator.Locator) {
	if err := l.Close(); err != nil {
		logger.Warn("error closing geo locator", logging.Pairs{keys.GeoLocator: name, keys.Error: err.Error()})
	}
}

var published atomic.Pointer[Set] // the serving Set, which the build-time gauge reads at scrape time

// Publish makes s the Set the build-time gauge reports
func Publish(s Set) {
	published.Store(&s)
}

var buildTimeDesc = prometheus.NewDesc(
	"trickster_geo_locator_build_timestamp_seconds",
	"When the data a geo locator has loaded was built, in seconds since the epoch.",
	[]string{keys.Geo_Locator}, nil,
)

type buildTimeCollector struct{}

func (buildTimeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- buildTimeDesc
}

func (buildTimeCollector) Collect(ch chan<- prometheus.Metric) {
	// read at scrape time, so a lookup costs nothing for it and a retired locator takes its series along
	p := published.Load()
	if p == nil {
		return
	}
	for name, r := range *p {
		bt, ok := r.Locator.(locator.BuildTimer)
		if !ok {
			continue
		}
		if t := bt.BuildTime(); !t.IsZero() {
			ch <- prometheus.MustNewConstMetric(buildTimeDesc, prometheus.GaugeValue, float64(t.Unix()), name)
		}
	}
}

func init() {
	prometheus.MustRegister(buildTimeCollector{})
}
