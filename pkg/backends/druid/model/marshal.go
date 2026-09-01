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

package model

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type renderedPoint struct {
	epoch      epoch.Epoch
	dimensions map[string]any
	values     map[string]any
	version    any
	rank       int64
	tagsKey    string
}

// MarshalTimeseries converts DataSet back into the native Druid response shape.
func MarshalTimeseries(ts timeseries.Timeseries, options *timeseries.RequestOptions,
	status int,
) ([]byte, error) {
	var buffer bytes.Buffer
	if err := MarshalTimeseriesWriter(ts, options, status, &buffer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// MarshalTimeseriesWriter writes DataSet in the native Druid response shape.
func MarshalTimeseriesWriter(ts timeseries.Timeseries, options *timeseries.RequestOptions,
	_ int, writer io.Writer,
) error {
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		return timeseries.ErrUnknownFormat
	}
	plan := planForMarshal(ds, options)
	if plan == nil {
		return timeseries.ErrUnknownFormat
	}
	points := renderedPoints(ds)
	var output any
	switch plan.QueryType() {
	case queryTimeseries:
		output = renderTimeseries(points, plan.Descending())
	case queryGroupBy:
		output = renderGroupBy(points, plan.Descending())
	case queryTopN:
		output = renderTopN(points, plan.Descending())
	default:
		return timeseries.ErrUnknownFormat
	}
	return json.NewEncoder(writer).Encode(output)
}

func planForMarshal(ds *dataset.DataSet, options *timeseries.RequestOptions) *QueryPlan {
	if options != nil {
		if plan, ok := options.ProviderRequest.(*QueryPlan); ok {
			return plan
		}
	}
	if ds.TimeRangeQuery != nil {
		if plan, ok := ds.TimeRangeQuery.ParsedQuery.(*QueryPlan); ok {
			return plan
		}
	}
	return nil
}

func renderedPoints(ds *dataset.DataSet) []renderedPoint {
	var out []renderedPoint
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			for _, point := range series.Points {
				rendered := renderedPoint{
					epoch: point.Epoch, dimensions: make(map[string]any),
					values: make(map[string]any), tagsKey: series.Header.Tags.JSON(),
				}
				for i, field := range series.Header.ValueFieldsList {
					var value any
					if i < len(point.Values) {
						value = point.Values[i]
					}
					switch field.ProviderData1 {
					case fieldNativeDimension:
						rendered.dimensions[field.Name] = value
					case fieldNativeVersion:
						rendered.version = value
					case fieldNativeRank:
						rendered.rank = numericRank(value)
					default:
						rendered.values[field.Name] = value
					}
				}
				out = append(out, rendered)
			}
		}
	}
	return out
}

func renderTimeseries(points []renderedPoint, descending bool) []map[string]any {
	sortRendered(points, descending, false)
	out := make([]map[string]any, len(points))
	for i, point := range points {
		out[i] = map[string]any{
			fieldTimestamp: formatTimestamp(point.epoch),
			"result":       point.values,
		}
	}
	return out
}

func renderGroupBy(points []renderedPoint, descending bool) []map[string]any {
	sortRendered(points, descending, true)
	out := make([]map[string]any, len(points))
	for i, point := range points {
		event := make(map[string]any, len(point.dimensions)+len(point.values))
		maps.Copy(event, point.dimensions)
		maps.Copy(event, point.values)
		row := map[string]any{fieldTimestamp: formatTimestamp(point.epoch), "event": event}
		if point.version != nil {
			row["version"] = point.version
		}
		out[i] = row
	}
	return out
}

func renderTopN(points []renderedPoint, descending bool) []map[string]any {
	sortRendered(points, descending, true)
	type bucket struct {
		epoch epoch.Epoch
		rows  []map[string]any
	}
	var buckets []bucket
	for _, point := range points {
		if len(buckets) == 0 || buckets[len(buckets)-1].epoch != point.epoch {
			buckets = append(buckets, bucket{epoch: point.epoch})
		}
		row := make(map[string]any, len(point.dimensions)+len(point.values))
		maps.Copy(row, point.dimensions)
		maps.Copy(row, point.values)
		buckets[len(buckets)-1].rows = append(buckets[len(buckets)-1].rows, row)
	}
	out := make([]map[string]any, len(buckets))
	for i, bucket := range buckets {
		out[i] = map[string]any{
			fieldTimestamp: formatTimestamp(bucket.epoch),
			"result":       bucket.rows,
		}
	}
	return out
}

func sortRendered(points []renderedPoint, descending, rank bool) {
	slices.SortStableFunc(points, func(a, b renderedPoint) int {
		if a.epoch != b.epoch {
			if (a.epoch < b.epoch) != descending {
				return -1
			}
			return 1
		}
		if rank && a.rank != b.rank {
			if a.rank < b.rank {
				return -1
			}
			return 1
		}
		if a.tagsKey < b.tagsKey {
			return -1
		}
		if a.tagsKey > b.tagsKey {
			return 1
		}
		return 0
	})
}

func formatTimestamp(value epoch.Epoch) string {
	timestamp := time.Unix(0, int64(value)).UTC()
	if timestamp.Nanosecond()%int(time.Millisecond) == 0 {
		return timestamp.Format("2006-01-02T15:04:05.000Z")
	}
	return timestamp.Format(time.RFC3339Nano)
}

func numericRank(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case uint64:
		if v > math.MaxInt64 {
			return math.MaxInt64
		}
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}
