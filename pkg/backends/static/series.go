/*
 * Copyright 2018 The Trickster Authors
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

package static

import (
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

// A backend's series are published only once it is in service, as one is also built just to validate a
// configuration. They carry over a reload that keeps the name, and are deleted after one that doesn't.

// responseSeries is a backend's response counters, by cache status and rendition
type responseSeries [numCacheStatuses][]prometheus.Counter

// newResponseSeries resolves every counter once, keeping label lookups off the request path
func newResponseSeries(resolve func(status, encoding string) prometheus.Counter) *responseSeries {
	var rs responseSeries
	for status, label := range cacheStatusLabels {
		rs[status] = make([]prometheus.Counter, maxRendition+1)
		for _, enc := range append([]providers.Provider{providers.Identity}, renditions...) {
			rs[status][enc] = resolve(label, encodingLabel(enc))
		}
	}
	return &rs
}

// unpublished returns a counter that belongs to no backend, and is never exported
func unpublished() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Name: "unpublished_total",
		Help: "Count kept by a static backend that is not in service. It is never registered.",
	})
}

func unpublishedResponses() *responseSeries {
	return newResponseSeries(func(string, string) prometheus.Counter { return unpublished() })
}

func publishedResponses(backend string) *responseSeries {
	return newResponseSeries(func(status, encoding string) prometheus.Counter {
		return metrics.FileserverResponses.WithLabelValues(backend, status, encoding)
	})
}

// seriesNames records which backend names have series in service, and which have been let
// go of and are waiting to see whether a replacement of the same name takes them over
var seriesNames = struct {
	mtx      sync.Mutex
	owners   map[string]*server
	released map[string]struct{}
}{owners: make(map[string]*server), released: make(map[string]struct{})}

// claimSeries puts a name's series in service, taking over any that were let go of
func claimSeries(name string, s *server) {
	seriesNames.mtx.Lock()
	defer seriesNames.mtx.Unlock()
	seriesNames.owners[name] = s
	delete(seriesNames.released, name)
}

// releaseSeries lets go of a name's series, unless a replacement has them already
func releaseSeries(name string, s *server) {
	seriesNames.mtx.Lock()
	defer seriesNames.mtx.Unlock()
	if seriesNames.owners[name] == s {
		delete(seriesNames.owners, name)
		seriesNames.released[name] = struct{}{}
	}
}

// sweepSeries deletes the series of every name that was let go of and not taken over, which
// is a backend that a reload removed or renamed. It is called once a reload's backends are in
// service, by when any of the same name has claimed them.
func sweepSeries() {
	seriesNames.mtx.Lock()
	defer seriesNames.mtx.Unlock()
	for name := range seriesNames.released {
		labels := prometheus.Labels{keys.Backend_Name: name}
		metrics.FileserverResponses.DeletePartialMatch(labels)
		metrics.FileserverCacheEvents.DeletePartialMatch(labels)
		delete(seriesNames.released, name)
	}
}
