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

// Package model converts between Graphite's render wire formats and the provider-neutral
// DataSet, rendering each client format as graphite-web 1.1.10's render/views.py does.
package model

import (
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// Field names used in the DataSet representation
const (
	TimestampFieldName = "timestamp"
	ValueFieldName     = "value"
)

// Output formats
const (
	FormatJSON    = "json"
	FormatRaw     = "raw"
	FormatCSV     = "csv"
	FormatMsgPack = "msgpack"
)

// DefaultConsolidationFunc is graphite-web's default consolidation function
const DefaultConsolidationFunc = "average"

// RenderOptions are the marshal-time parameters of a render request: everything that
// affects how the cached series is serialized to this client, none of it in the cache key
type RenderOptions struct {
	// Format is json (default), raw, csv or msgpack
	Format string
	// MaxDataPoints consolidates the series (JSON only), 0 for none
	MaxDataPoints int
	// NoNullPoints drops null datapoints (JSON only)
	NoNullPoints bool
	// JSONP wraps the JSON in a callback
	JSONP string
	// Pretty indents the JSON
	Pretty bool
	// XFilesFactor is the request's xFilesFactor, used by consolidation
	XFilesFactor float64
	// Location renders CSV timestamps (the request's tz, else the origin's)
	Location *time.Location
	// PathExpressions, when set, are the target expressions the series were
	// fetched with, in series order; msgpack reports them as pathExpression
	PathExpressions []string
}

// RenderOptionsProvider is implemented by the provider request carried on
// RequestOptions.ProviderRequest
type RenderOptionsProvider interface {
	RenderOptions() RenderOptions
}

// extracts the RenderOptions from a RequestOptions, defaulting to compact JSON
func renderOptions(rlo *timeseries.RequestOptions) RenderOptions {
	if rlo != nil {
		switch p := rlo.ProviderRequest.(type) {
		case RenderOptionsProvider:
			return p.RenderOptions()
		case RenderOptions:
			return p
		case *RenderOptions:
			if p != nil {
				return *p
			}
		}
	}
	return RenderOptions{}
}

// NewModeler returns a collection of modeling functions for Graphite
// interoperability
func NewModeler() *timeseries.Modeler {
	return &timeseries.Modeler{
		WireUnmarshaler:       UnmarshalTimeseries,
		WireUnmarshalerReader: UnmarshalTimeseriesReader,
		WireMarshaler:         MarshalTimeseries,
		WireMarshalWriter:     MarshalTimeseriesWriter,
		CacheMarshaler:        dataset.MarshalDataSet,
		CacheUnmarshaler:      dataset.UnmarshalDataSet,
	}
}
