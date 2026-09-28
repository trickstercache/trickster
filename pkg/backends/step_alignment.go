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
	providers.Graphite:   timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate,
	providers.ClickHouse: timeseries.StepAlignmentOff | timeseries.StepAlignmentDrop,
	// off is applied wherever a query supports it; a query that doesn't keeps its default
	providers.InfluxDB:    timeseries.StepAlignmentOff,
	providers.Druid:       timeseries.StepAlignmentOff,
	providers.MySQL:       timeseries.StepAlignmentOff | timeseries.StepAlignmentDrop,
	providers.Postgres:    timeseries.StepAlignmentOff | timeseries.StepAlignmentDrop,
	providers.TimescaleDB: timeseries.StepAlignmentOff | timeseries.StepAlignmentDrop,
}

// ValidateStepAlignment checks a backend's configured step alignment against the modes its
// provider supports and the modes it can apply
func ValidateStepAlignment(b Backend, o *bo.Options) error {
	if o == nil || o.StepAlignment == 0 {
		return nil
	}
	// options built in code can hold any bits; each request would reject them and fall back
	if !o.StepAlignment.IsMode() {
		return bo.NewErrInvalidStepAlignment(o.StepAlignment, o.Name)
	}
	sa, ok := b.(timeseries.StepAligner)
	if !ok {
		if o.Provider == providers.ALB {
			return bo.NewErrStepAlignmentNotImplemented(o.StepAlignment, o.Provider, o.Name)
		}
		return bo.NewErrUnsupportedStepAlignment(o.StepAlignment, 0, o.Provider, o.Name)
	}
	if supported, _ := sa.StepAlignments(); supported&o.StepAlignment == 0 {
		return bo.NewErrUnsupportedStepAlignment(o.StepAlignment, supported, o.Provider, o.Name)
	}
	// a supported mode is refused, rather than ignored, until the provider applies it on every path
	if stepAlignmentsApplied[o.Provider]&o.StepAlignment == 0 {
		return bo.NewErrStepAlignmentNotImplemented(o.StepAlignment, o.Provider, o.Name)
	}
	return nil
}
