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
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

const millisecondsPerSecond = float64(time.Second / time.Millisecond)

func finalizeScalarBinaryWrapper(ds *dataset.DataSet, query string,
	wrapper promql.ScalarBinaryWrapper,
) {
	histogramOperations, _ := ds.ValueOperations.(promql.HistogramScalarOperations)
	dropMetricName := wrapper.DropsMetricName()
	usesEvaluationTime := wrapper.UsesEvaluationTime()
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		rows := rewriteFirstValues(result.SeriesList, func(series *dataset.Series, seg *dataset.Segment, i int,
			dst []byte,
		) ([]byte, bool) {
			if seg.NumCols() == 0 || seg.KindAt(0, i) != dataset.KindString {
				return dst, false
			}
			oldValue := seg.Text(0, i)
			var evaluationTime float64
			if usesEvaluationTime {
				evaluationTime = float64(int64(seg.Epoch(i))/int64(time.Millisecond)) /
					millisecondsPerSecond
			}
			if isHistogramSeries(series) {
				updated, keep := wrapper.ApplyHistogram(oldValue, histogramOperations, evaluationTime)
				value, ok := updated.(string)
				if !keep || !ok {
					return dst, false
				}
				return append(dst, value...), true
			}
			parsed, err := strconv.ParseFloat(oldValue, 64)
			if err != nil {
				return dst, false
			}
			updated, keep := wrapper.ApplyFloat(parsed, evaluationTime)
			if !keep {
				return dst, false
			}
			return strconv.AppendFloat(dst, updated, 'f', -1, 64), true
		})
		keptSeries := result.SeriesList[:0]
		for si, series := range result.SeriesList {
			if rows[si] == nil {
				continue
			}
			series.SetSegments(rows[si])
			series.Header.QueryStatement = query
			if dropMetricName {
				series.Header.Name = ""
				delete(series.Header.Tags, promql.MetricNameLabel)
			}
			series.Header.CalculateHash(true)
			series.Header.CalculateSize()
			keptSeries = append(keptSeries, series)
		}
		result.SeriesList = keptSeries
	}
}
