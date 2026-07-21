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
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type limitKLogicalSeries struct {
	group   string
	members []*dataset.Series
}

func finalizeLimitKAggregation(ds *dataset.DataSet, spec promql.LimitKAggregation) {
	isRangeQuery := ds.Step() > 0
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	if dataSetContainsQueryStatement(ds, spec.InnerQuery) {
		for _, result := range ds.Results {
			if result != nil {
				finalizeLimitKResult(result, spec)
			}
		}
	}

	if !spec.SortSet {
		return
	}
	if isRangeQuery {
		appendWarningOnce(ds, sortInRangeQueryWarning)
	}
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		// PromQL sort functions ignore native-histogram samples even though
		// limitk itself retains them.
		result.SeriesList = filterHistogramSeries(result.SeriesList)
		if !isRangeQuery {
			sortInstantSeries(result.SeriesList, spec.SortDescending)
		}
	}
}

func finalizeLimitKResult(result *dataset.Result, spec promql.LimitKAggregation) {
	if spec.K < 1 {
		result.SeriesList = result.SeriesList[:0]
		return
	}

	// TSM's merge layer orders series by the JSON serialization of their full
	// label set. Reapply that existing order so direct finalizer users and all
	// fanout completion schedules have the same first-visited contract.
	result.SeriesList.SortByTags()
	logicalOrder := make([]*limitKLogicalSeries, 0, len(result.SeriesList))
	lastTagsKey := ""
	for _, series := range result.SeriesList {
		if series == nil || len(series.Points) == 0 {
			continue
		}
		tagsKey := series.Header.Tags.JSON()
		if len(logicalOrder) == 0 || tagsKey != lastTagsKey {
			logicalOrder = append(logicalOrder, &limitKLogicalSeries{
				group: rankGroupKey(series.Header.Tags, spec.Grouping),
			})
			lastTagsKey = tagsKey
		}
		last := logicalOrder[len(logicalOrder)-1]
		last.members = append(last.members, series)
	}

	selectedCounts := make(map[rankBucketKey]int64)
	for _, logical := range logicalOrder {
		selectLimitKLogicalPoints(logical, spec.K, selectedCounts)
	}
	kept := result.SeriesList[:0]
	for _, series := range result.SeriesList {
		if series != nil && len(series.Points) > 0 {
			kept = append(kept, series)
		}
	}
	result.SeriesList = kept
}

func selectLimitKLogicalPoints(logical *limitKLogicalSeries, k int64,
	selectedCounts map[rankBucketKey]int64,
) {
	indexes := make([]int, len(logical.members))
	kept := make([]dataset.Points, len(logical.members))
	for i, series := range logical.members {
		kept[i] = series.Points[:0]
	}

	for {
		var pointEpoch epoch.Epoch
		found := false
		for i, series := range logical.members {
			if indexes[i] >= len(series.Points) {
				continue
			}
			candidateEpoch := series.Points[indexes[i]].Epoch
			if !found || candidateEpoch < pointEpoch {
				pointEpoch = candidateEpoch
				found = true
			}
		}
		if !found {
			break
		}

		bucket := rankBucketKey{epoch: int64(pointEpoch), group: logical.group}
		selected := selectedCounts[bucket] < k
		if selected {
			selectedCounts[bucket]++
		}
		for i, series := range logical.members {
			for indexes[i] < len(series.Points) &&
				series.Points[indexes[i]].Epoch == pointEpoch {
				point := series.Points[indexes[i]]
				if selected {
					kept[i] = append(kept[i], point)
				}
				indexes[i]++
			}
		}
	}

	for i, series := range logical.members {
		series.Points = kept[i]
		series.PointSize = kept[i].Size()
	}
}
