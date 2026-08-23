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

package graphite

import (
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

// metricsObserver records resolution events into the Graphite metric
// families frozen in implementation plan item 3.4. Every label value it
// emits comes from the closed sets defined in the resolution package, so no
// metric path, target expression or query text can reach a label.
type metricsObserver struct {
	name string
}

var _ resolution.Observer = &metricsObserver{}

// newObserver returns an observer bound to a backend name
func newObserver(name string) *metricsObserver {
	return &metricsObserver{name: name}
}

// Lookup records one step-resolution outcome
func (o *metricsObserver) Lookup(confidence, source string) {
	metrics.GraphiteResolutionLookups.WithLabelValues(o.name, confidence, source).Inc()
}

// Probe records one synthetic request issued to learn a ladder
func (o *metricsObserver) Probe(kind, result string) {
	metrics.GraphiteProbes.WithLabelValues(o.name, kind, result).Inc()
}

// Ladders reports how many distinct complete ladders are known
func (o *metricsObserver) Ladders(n int) {
	metrics.GraphiteLadders.WithLabelValues(o.name).Set(float64(n))
}

// RegistryEntries reports the size of one registry layer
func (o *metricsObserver) RegistryEntries(layer string, n int) {
	metrics.GraphiteRegistryEntries.WithLabelValues(o.name, layer).Set(float64(n))
}

// Fallback records one render request routed to the unaccelerated lane
func (o *metricsObserver) Fallback(reason string) {
	metrics.GraphiteFallbacks.WithLabelValues(o.name, reason).Inc()
}

// Misprediction records a response whose step contradicted the prediction.
// This must stay at zero: it means a ladder in the registry was wrong.
func (o *metricsObserver) Misprediction() {
	metrics.GraphiteStepMispredictions.WithLabelValues(o.name).Inc()
}
