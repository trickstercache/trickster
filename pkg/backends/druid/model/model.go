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

// Package model converts Apache Druid native and SQL responses to and from DataSet.
package model

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// QueryPlan is the immutable provider plan shared by DPC extent rewrites and
// wire modeling. All slice fields are copied at construction and on access.
type QueryPlan struct {
	queryType   string
	dimensions  []string
	valueFields []string
	descending  bool
	renderStart []byte
	renderEnd   []byte
}

// SQLQueryPlan marks a Druid SQL request and carries the shared immutable SQL
// extent plan. The marker keeps Druid's tabular wire model and native model
// separate while allowing both to use one timeseries.Modeler.
type SQLQueryPlan struct {
	Plan          *sqlanalyzer.QueryPlan
	cacheKeyBody  []byte
	outputColumns []string
	format        byte
	header        bool
}

const (
	dataSetStatusSuccess = "success"
	sqlResultName        = "sql"

	// SQLResponseObject is Druid's object result format. It is the default
	// format used by the public constructor and by requests without a format.
	SQLResponseObject byte = iota
	// SQLResponseArray is Druid's array result format. It is useful for the
	// Grafana Druid plugin, which requests an explicit column-name header.
	SQLResponseArray
)

// NewSQLQueryPlan wraps a shared SQL plan for Druid's object result format.
// cacheKeyBody is the canonical request envelope, before an extent rewrite.
func NewSQLQueryPlan(plan *sqlanalyzer.QueryPlan, cacheKeyBody []byte) *SQLQueryPlan {
	return &SQLQueryPlan{Plan: plan, cacheKeyBody: bytes.Clone(cacheKeyBody)}
}

// NewSQLQueryPlanWithResponseShape wraps a shared SQL plan and remembers the
// Druid response envelope requested by the client. Only object responses and
// array responses with a column-name header are modeled by the provider.
func NewSQLQueryPlanWithResponseShape(plan *sqlanalyzer.QueryPlan, cacheKeyBody []byte,
	format byte, header bool, outputColumns ...string,
) *SQLQueryPlan {
	return &SQLQueryPlan{
		Plan: plan, cacheKeyBody: bytes.Clone(cacheKeyBody),
		outputColumns: slices.Clone(outputColumns), format: format, header: header,
	}
}

// ResponseFormat reports the Druid SQL result format carried by the plan.
func (p *SQLQueryPlan) ResponseFormat() byte {
	if p == nil {
		return SQLResponseObject
	}
	return p.format
}

// Header reports whether the array response includes a column-name header.
func (p *SQLQueryPlan) Header() bool {
	return p != nil && p.header
}

// OutputColumns returns the SQL select-list names in wire order. Druid's
// array response uses this order for its column-name header.
func (p *SQLQueryPlan) OutputColumns() []string {
	if p == nil {
		return nil
	}
	return slices.Clone(p.outputColumns)
}

// CacheKeyBody returns the stable body identity for this provider-owned
// request. DPC rewrites a separate upstream body for each extent; exposing the
// pre-rewrite envelope keeps those transport details out of the cache key.
func (p *SQLQueryPlan) CacheKeyBody() []byte {
	if p == nil {
		return nil
	}
	return bytes.Clone(p.cacheKeyBody)
}

// NewQueryPlan constructs an immutable Druid query plan.
func NewQueryPlan(queryType string, dimensions, valueFields []string, descending bool,
	renderStart, renderEnd []byte,
) *QueryPlan {
	return &QueryPlan{
		queryType:   queryType,
		dimensions:  slices.Clone(dimensions),
		valueFields: slices.Clone(valueFields),
		descending:  descending,
		renderStart: bytes.Clone(renderStart),
		renderEnd:   bytes.Clone(renderEnd),
	}
}

// ValueFields returns a copy of the declared aggregation output names.
func (p *QueryPlan) ValueFields() []string {
	if p == nil {
		return nil
	}
	return slices.Clone(p.valueFields)
}

// QueryType returns the native Druid query type.
func (p *QueryPlan) QueryType() string {
	if p == nil {
		return ""
	}
	return p.queryType
}

// Dimensions returns a copy of the dimension output names.
func (p *QueryPlan) Dimensions() []string {
	if p == nil {
		return nil
	}
	return slices.Clone(p.dimensions)
}

// Descending reports whether response buckets should be rendered newest first.
func (p *QueryPlan) Descending() bool {
	return p != nil && p.descending
}

// RenderInterval returns a fresh JSON query body with the provided half-open
// interval. It never mutates the shared plan.
func (p *QueryPlan) RenderInterval(interval string) ([]byte, error) {
	if p == nil || p.renderStart == nil || p.renderEnd == nil {
		return nil, timeseries.ErrInvalidBody
	}
	b, err := json.Marshal([]string{interval})
	if err != nil {
		return nil, err
	}
	// Build from a cloned prefix instead of summing attacker-controlled slice
	// lengths for a capacity hint. The append operations grow the buffer with
	// the runtime's overflow checks while preserving the immutable plan.
	out := bytes.Clone(p.renderStart)
	out = append(out, b...)
	out = append(out, p.renderEnd...)
	return out, nil
}

// NewModeler returns the Druid wire and stock DataSet cache modelers.
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
