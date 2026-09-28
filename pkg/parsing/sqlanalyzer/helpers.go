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

package sqlanalyzer

import (
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// ObjectAnalysis returns a failed-delta Analysis that remains eligible for the
// object proxy cache, carrying the fail-closed reason and error.
func ObjectAnalysis(reason AnalysisReason, err error) Analysis {
	return Analysis{Mode: CacheModeObject, Reason: reason, Err: err}
}

// FlattenConjunction appends the conjunct leaves of an AND tree to out.
// normalize (optional) strips grouping wrappers; split decomposes an AND node.
func FlattenConjunction[E any](expr E, normalize func(E) E,
	split func(E) (E, E, bool), out []E,
) []E {
	if normalize != nil {
		expr = normalize(expr)
	}
	if left, right, ok := split(expr); ok {
		out = FlattenConjunction(left, normalize, split, out)
		return FlattenConjunction(right, normalize, split, out)
	}
	return append(out, expr)
}

// UnixTime converts an integer epoch value in the precision indicated by unit
// to a time. Non-epoch units are interpreted as Unix seconds.
func UnixTime(value int64, unit timeseries.FieldDataType) time.Time {
	switch unit {
	case timeseries.DateTimeUnixMilli:
		return time.Unix(value/1_000, (value%1_000)*int64(time.Millisecond))
	case timeseries.DateTimeUnixMicro:
		return time.Unix(value/1_000_000, (value%1_000_000)*int64(time.Microsecond))
	case timeseries.DateTimeUnixNano:
		return time.Unix(0, value)
	default:
		return time.Unix(value, 0)
	}
}

// ParseSQLTime parses a SQL datetime, date, or RFC3339 string literal as UTC,
// unescaping doubled single quotes first.
func ParseSQLTime(value string) (time.Time, bool) {
	value = strings.ReplaceAll(value, "''", "'")
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02", time.RFC3339} {
		parsed, err := time.ParseInLocation(layout, value, time.UTC)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// SafeUnixSeconds reports whether a Unix-seconds value stays representable as
// Unix nanoseconds, avoiding silent wraparound in downstream extent math.
func SafeUnixSeconds(value int64) bool {
	const maxUnixNanoSeconds = int64(1<<63-1) / int64(time.Second)
	return value >= -maxUnixNanoSeconds && value <= maxUnixNanoSeconds
}

// NewTimeRangeQuery returns a TimeRangeQuery keyed on the raw statement,
// suitable for both object-cache fallbacks and delta plans.
func NewTimeRangeQuery(statement string) *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{
		Statement:        statement,
		CacheKeyElements: map[string]string{"query": statement},
	}
}

// ApplyToQuery copies the plan's dialect-independent facts onto trq: canonical
// statement and cache key, cadence, field definitions, and the plan itself.
func (p *QueryPlan) ApplyToQuery(trq *timeseries.TimeRangeQuery) {
	trq.Statement = p.CanonicalSQL
	if trq.CacheKeyElements == nil {
		trq.CacheKeyElements = make(map[string]string, 1)
	}
	trq.CacheKeyElements["query"] = p.CanonicalSQL
	trq.Step = p.Step
	trq.StepNS = p.Step.Nanoseconds()
	trq.Phase = p.Phase
	trq.SampleModel = timeseries.SampleModelBucket
	trq.StepAlignments, trq.StepAlignment = StepAlignments, DefaultStepAlignment
	trq.BackfillTolerance = p.BackfillTolerance
	trq.TimestampDefinition = timeseries.FieldDefinition{
		Name:          p.OutputColumn,
		DataType:      p.OutputUnit,
		Role:          timeseries.RoleTimestamp,
		ProviderData1: byte(p.InputUnit),
	}
	trq.TagFieldDefintions = make(timeseries.FieldDefinitions, len(p.GroupColumns))
	for i, name := range p.GroupColumns {
		trq.TagFieldDefintions[i] = timeseries.FieldDefinition{Name: name, Role: timeseries.RoleTag}
	}
	trq.Ordering = append(trq.Ordering[:0], p.Ordering...)
	trq.ParsedQuery = p
}

// RequestExtent converts the plan's comparator-preserving bounds into the
// inclusive bucket extent convention. A missing upper bound resolves to now.
func (p *QueryPlan) RequestExtent(now time.Time) timeseries.Extent {
	var extent timeseries.Extent
	if p.LowerBound != nil {
		extent.Start = p.LowerBound.Value
		if !p.LowerBound.Inclusive {
			extent.Start = extent.Start.Add(p.Step)
		}
	}
	if p.UpperBound == nil {
		extent.End = now
	} else {
		extent.End = p.UpperBound.Value
		if !p.UpperBound.Inclusive {
			extent.End = extent.End.Add(-p.Step)
		}
	}
	return extent
}

// RequestedRange returns the plan's raw bounds as the client's range, or its rounded bucket extent
// when it has none; a missing upper bound is open-ended and ends at now
func (p *QueryPlan) RequestedRange(now time.Time) timeseries.RequestedRange {
	var r timeseries.RequestedRange
	if p.RawLower == nil {
		e := p.RequestExtent(now)
		r.Start, r.End, r.OpenEnded = e.Start, e.End.Add(p.Step), p.UpperBound == nil
		if r.OpenEnded {
			r.End = now
		}
		return r
	}
	r.Start, r.StartExclusive = p.RawLower.Value, !p.RawLower.Inclusive
	if p.RawUpper == nil {
		r.End, r.OpenEnded = now, true
	} else {
		r.End, r.EndInclusive = p.RawUpper.Value, p.RawUpper.Inclusive
	}
	return r
}
