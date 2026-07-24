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

package prometheus

import (
	"math"

	"github.com/cespare/xxhash/v2"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func finalizeLimitRatio(ds *dataset.DataSet, spec promql.LimitRatioAggregation) {
	isRangeQuery := ds.Step() > 0

	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	if isRangeQuery && spec.SortSet {
		appendWarningOnce(ds, sortInRangeQueryWarning)
	}
	for _, result := range ds.Results {
		if result == nil || len(result.SeriesList) == 0 {
			continue
		}
		selected := result.SeriesList[:0]
		// Prometheus hashes each complete label set; by/without only partitions
		// candidates and does not change this per-series threshold decision.
		for _, series := range result.SeriesList {
			if series == nil || len(series.Points) == 0 ||
				!limitRatioSelectHash(spec.Ratio, prometheusLabelsHash(series.Header.Tags)) {
				continue
			}
			selected = append(selected, series)
		}
		result.SeriesList = selected
		if spec.SortSet {
			result.SeriesList = filterHistogramSeries(result.SeriesList)
			if !isRangeQuery {
				sortInstantSeries(result.SeriesList, spec.SortDescending)
			}
		}
	}
}

// The default Prometheus stringlabels implementation hashes sorted,
// length-prefixed label names and values as one xxhash byte stream.
func prometheusLabelsHash(tags dataset.Tags) uint64 {
	keys := tags.Keys()
	size := 0
	for _, key := range keys {
		size += prometheusEncodedStringSize(len(key)) +
			prometheusEncodedStringSize(len(tags[key]))
	}
	buf := make([]byte, 0, size)
	for _, key := range keys {
		buf = appendPrometheusStringSize(buf, len(key))
		buf = append(buf, key...)
		buf = appendPrometheusStringSize(buf, len(tags[key]))
		buf = append(buf, tags[key]...)
	}
	return xxhash.Sum64(buf)
}

func prometheusEncodedStringSize(size int) int {
	if size < 255 {
		return size + 1
	}
	return size + 4
}

func appendPrometheusStringSize(buf []byte, size int) []byte {
	if size < 255 {
		return append(buf, byte(size)) // #nosec G115 -- size is bounded above by 254
	}
	return append(buf, 255,
		byte(size), byte(size>>8), byte(size>>16), // #nosec G115 -- each encoded byte is intentional
	)
}

func limitRatioSelectHash(ratio float64, hash uint64) bool {
	offset := float64(hash) / float64(math.MaxUint64)
	if ratio >= 0 {
		return offset < ratio
	}
	return offset >= 1+ratio
}
