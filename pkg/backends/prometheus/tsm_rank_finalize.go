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
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

const (
	rankOperatorBottomK = "bottomk"
	rankOperatorTopK    = "topk"
)

type rankBucketKey struct {
	epoch int64
	group string
}

type rankCandidate struct {
	series   *dataset.Series
	pointIdx int
	value    float64
	tagsJSON string
}

// FinalizeTSMMerge applies Prometheus-only merge finalization after TSM fanout
// has accumulated the rewritten inner-query responses. The rank step belongs
// here so topk/bottomk operate on globally merged values, not per-backend ranks.
func (c *Client) FinalizeTSMMerge(query string, ts timeseries.Timeseries) {
	spec, ok := promql.ParseRankAggregation(query)
	if !ok {
		return
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		return
	}
	finalizeRankAggregation(ds, spec)
}

func finalizeRankAggregation(ds *dataset.DataSet, spec promql.RankAggregation) {
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	for _, result := range ds.Results {
		if result == nil || len(result.SeriesList) == 0 {
			continue
		}
		buckets := make(map[rankBucketKey][]rankCandidate)
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			group := rankGroupKey(series.Header.Tags, spec.Grouping)
			tagsJSON := series.Header.Tags.JSON()
			for i, point := range series.Points {
				value, ok := rankPointValue(point)
				if !ok {
					continue
				}
				key := rankBucketKey{epoch: int64(point.Epoch), group: group}
				buckets[key] = append(buckets[key], rankCandidate{
					series:   series,
					pointIdx: i,
					value:    value,
					tagsJSON: tagsJSON,
				})
			}
		}

		selected := make(map[*dataset.Series]map[int]struct{})
		for _, candidates := range buckets {
			sortRankCandidates(candidates, spec.Operator)
			limit := min(spec.K, len(candidates))
			for _, candidate := range candidates[:limit] {
				if selected[candidate.series] == nil {
					selected[candidate.series] = make(map[int]struct{})
				}
				selected[candidate.series][candidate.pointIdx] = struct{}{}
			}
		}
		result.SeriesList = keepSelectedRankPoints(result.SeriesList, selected)
		sortRankSeriesIfInstant(result.SeriesList, spec)
	}
}

func rankPointValue(point dataset.Point) (float64, bool) {
	if len(point.Values) == 0 {
		return 0, false
	}
	v, ok := point.Values[0].(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func sortRankCandidates(candidates []rankCandidate, operator string) {
	slices.SortStableFunc(candidates, func(a, b rankCandidate) int {
		if rankValueLess(a.value, b.value, operator) {
			return -1
		}
		if rankValueLess(b.value, a.value, operator) {
			return 1
		}
		return strings.Compare(a.tagsJSON, b.tagsJSON)
	})
}

func rankValueLess(a, b float64, operator string) bool {
	aNaN, bNaN := math.IsNaN(a), math.IsNaN(b)
	if aNaN || bNaN {
		if aNaN && bNaN {
			return false
		}
		return !aNaN
	}
	if operator == rankOperatorBottomK {
		return a < b
	}
	return a > b
}

func rankGroupKey(tags dataset.Tags, grouping promql.AggregationGrouping) string {
	if len(grouping.Labels) == 0 {
		if grouping.Without {
			return tags.String()
		}
		return ""
	}
	labelSet := make(map[string]struct{}, len(grouping.Labels))
	for _, label := range grouping.Labels {
		labelSet[label] = struct{}{}
	}
	if grouping.Without {
		keys := tags.Keys()
		kept := make([]string, 0, len(keys))
		for _, key := range keys {
			if _, skip := labelSet[key]; !skip {
				kept = append(kept, key+"="+tags[key])
			}
		}
		return strings.Join(kept, ";")
	}
	parts := make([]string, 0, len(grouping.Labels))
	for _, label := range grouping.Labels {
		parts = append(parts, label+"="+tags[label])
	}
	return strings.Join(parts, ";")
}

func keepSelectedRankPoints(
	seriesList dataset.SeriesList, selected map[*dataset.Series]map[int]struct{},
) dataset.SeriesList {
	keptSeries := seriesList[:0]
	for _, series := range seriesList {
		if series == nil {
			continue
		}
		selectedPoints := selected[series]
		if len(selectedPoints) == 0 {
			continue
		}
		keptPoints := series.Points[:0]
		for i, point := range series.Points {
			if _, ok := selectedPoints[i]; ok {
				keptPoints = append(keptPoints, point)
			}
		}
		if len(keptPoints) == 0 {
			continue
		}
		series.Points = keptPoints
		series.PointSize = series.Points.Size()
		keptSeries = append(keptSeries, series)
	}
	return keptSeries
}

func sortRankSeriesIfInstant(seriesList dataset.SeriesList, spec promql.RankAggregation) {
	if len(seriesList) < 2 {
		return
	}
	if seriesList[0] == nil || len(seriesList[0].Points) != 1 {
		return
	}
	epoch := seriesList[0].Points[0].Epoch
	for _, series := range seriesList {
		if series == nil || len(series.Points) != 1 || series.Points[0].Epoch != epoch {
			return
		}
	}
	desc := spec.Operator == rankOperatorTopK
	if spec.SortSet {
		desc = spec.SortDescending
	}
	slices.SortStableFunc(seriesList, func(a, b *dataset.Series) int {
		iv, iok := rankPointValue(a.Points[0])
		jv, jok := rankPointValue(b.Points[0])
		if !iok || !jok {
			if iok {
				return -1
			}
			if jok {
				return 1
			}
			return 0
		}
		op := rankOperatorBottomK
		if desc {
			op = rankOperatorTopK
		}
		if rankValueLess(iv, jv, op) {
			return -1
		}
		if rankValueLess(jv, iv, op) {
			return 1
		}
		return strings.Compare(a.Header.Tags.JSON(), b.Header.Tags.JSON())
	})
}
