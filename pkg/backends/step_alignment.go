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

package backends

import (
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

var stepAlignmentsApplied = map[string]timeseries.StepAlignment{
	providers.Prometheus: timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate |
		timeseries.StepAlignmentPartialEnd,
	providers.Graphite: timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate,
	// a query that doesn't support the configured mode, such as Flux under partial, keeps its default
	providers.ClickHouse:  timeseries.StepAlignmentAll,
	providers.InfluxDB:    timeseries.StepAlignmentAll,
	providers.Druid:       timeseries.StepAlignmentAll,
	providers.MySQL:       timeseries.StepAlignmentOff | timeseries.StepAlignmentDrop,
	providers.Postgres:    timeseries.StepAlignmentOff | timeseries.StepAlignmentDrop,
	providers.TimescaleDB: timeseries.StepAlignmentOff | timeseries.StepAlignmentDrop,
}

var nativeStepAlignmentsApplied = map[string]timeseries.StepAlignment{
	// narrows the modes of a backend a native listener also serves, where it applies fewer of them
	providers.InfluxDB: timeseries.StepAlignmentOff | timeseries.StepAlignmentDrop,
}

// ValidateStepAlignment checks a backend's configured step alignment against the modes its
// provider supports and applies, including on a native listener that serves it
func ValidateStepAlignment(b Backend, o *bo.Options, nativeListener bool) error {
	if o == nil || o.StepAlignment == 0 {
		return nil
	}
	// options built in code can hold any bits; each request would reject them and fall back
	if !o.StepAlignment.IsMode() {
		return bo.NewErrInvalidStepAlignment(o.StepAlignment, o.Name)
	}
	if o.Provider == providers.ALB {
		// an ALB applies its mode to its members, which the ALB validates once its pool is known
		return nil
	}
	sa, ok := b.(timeseries.StepAligner)
	if !ok {
		return bo.NewErrUnsupportedStepAlignment(o.StepAlignment, 0, o.Provider, o.Name)
	}
	if supported, _ := sa.StepAlignments(); supported&o.StepAlignment == 0 {
		return bo.NewErrUnsupportedStepAlignment(o.StepAlignment, supported, o.Provider, o.Name)
	}
	// a supported mode is refused, rather than ignored, until the provider applies it on every path
	applied := stepAlignmentsApplied[o.Provider]
	if narrowed, ok := nativeStepAlignmentsApplied[o.Provider]; ok && nativeListener {
		applied &= narrowed
	}
	if applied&o.StepAlignment == 0 {
		return bo.NewErrStepAlignmentNotImplemented(o.StepAlignment, o.Provider, o.Name)
	}
	return nil
}

// StepAlignmentProfile returns the mode a backend applies when no request names one, and the modes
// it can be told to apply; both are zero for a backend that applies no step alignment
func StepAlignmentProfile(b Backend) (effective, applicable timeseries.StepAlignment) {
	if b == nil {
		return 0, 0
	}
	sa, ok := b.(timeseries.StepAligner)
	o := b.Configuration()
	if !ok || o == nil {
		return 0, 0
	}
	supported, def := sa.StepAlignments()
	effective = def
	if o.StepAlignment != 0 {
		effective = o.StepAlignment
	}
	return effective, supported & stepAlignmentsApplied[o.Provider]
}
