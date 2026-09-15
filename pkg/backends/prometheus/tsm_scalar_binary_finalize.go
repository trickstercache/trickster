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
		keptSeries := result.SeriesList[:0]
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			keptPoints := series.Points[:0]
			for _, point := range series.Points {
				if len(point.Values) == 0 {
					continue
				}
				oldValue, ok := point.Values[0].(string)
				if !ok {
					continue
				}
				var evaluationTime float64
				if usesEvaluationTime {
					evaluationTime = float64(int64(point.Epoch)/int64(time.Millisecond)) /
						millisecondsPerSecond
				}
				var value string
				if isHistogramSeries(series) {
					updated, keep := wrapper.ApplyHistogram(oldValue, histogramOperations,
						evaluationTime)
					value, ok = updated.(string)
					if !keep || !ok {
						continue
					}
				} else {
					parsed, err := strconv.ParseFloat(oldValue, 64)
					if err != nil {
						continue
					}
					updated, keep := wrapper.ApplyFloat(parsed, evaluationTime)
					if !keep {
						continue
					}
					value = strconv.FormatFloat(updated, 'f', -1, 64)
				}
				point.Size += len(value) - len(oldValue)
				point.Values[0] = value
				keptPoints = append(keptPoints, point)
			}
			if len(keptPoints) == 0 {
				continue
			}
			series.Points = keptPoints
			series.PointSize = keptPoints.Size()
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
