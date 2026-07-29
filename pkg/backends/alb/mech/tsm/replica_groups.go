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

package tsm

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

type replicaGroup struct {
	id         string
	configured pool.Targets
	live       []int
}

func effectiveReplicaGroup(t *pool.Target, index int) string {
	if t != nil {
		if group := t.ReplicaGroup(); group != "" {
			return group
		}
		if name := t.Name(); name != "" {
			return name
		}
	}
	return fmt.Sprintf("\x00member-%d", index)
}

// replicaTopology preserves configured group order and member order. Live
// target indexes continue to address fanout result slots, even after health
// filtering changes which configured members are present.
func replicaTopology(live, configured pool.Targets) []replicaGroup {
	if len(configured) == 0 {
		configured = live
	}
	groups := make([]replicaGroup, 0, len(configured))
	indexes := make(map[string]int, len(configured))
	targetGroup := make(map[*pool.Target]string, len(configured))
	for i, target := range configured {
		id := effectiveReplicaGroup(target, i)
		targetGroup[target] = id
		groupIndex, ok := indexes[id]
		if !ok {
			groupIndex = len(groups)
			indexes[id] = groupIndex
			groups = append(groups, replicaGroup{id: id})
		}
		groups[groupIndex].configured = append(groups[groupIndex].configured, target)
	}
	for i, target := range live {
		id, ok := targetGroup[target]
		if !ok {
			id = effectiveReplicaGroup(target, len(configured)+i)
		}
		groupIndex, ok := indexes[id]
		if !ok {
			groupIndex = len(groups)
			indexes[id] = groupIndex
			groups = append(groups, replicaGroup{id: id})
		}
		groups[groupIndex].live = append(groups[groupIndex].live, i)
	}
	return groups
}

func coalesceReplicaContributions(
	ctx context.Context,
	live, configured pool.Targets,
	contributions []*gatherContribution,
	variant string,
	toleranceNanos int64,
) ([]*gatherContribution, []string, bool) {
	groups := replicaTopology(live, configured)
	logical := make([]*gatherContribution, 0, len(groups))
	warnings := make([]string, 0)
	var hasFailure bool
	for _, group := range groups {
		if ctx.Err() != nil {
			return logical, warnings, hasFailure
		}
		metrics.ALBTSMReplicaEvents.WithLabelValues("group_attempted", variant).Inc()
		usable := make([]*gatherContribution, 0, len(group.live))
		selectedLiveIndex := -1
		for _, liveIndex := range group.live {
			if liveIndex >= len(contributions) || contributions[liveIndex] == nil {
				continue
			}
			if selectedLiveIndex < 0 {
				selectedLiveIndex = liveIndex
			}
			usable = append(usable, contributions[liveIndex])
		}
		if len(usable) == 0 {
			hasFailure = true
			metrics.ALBTSMReplicaEvents.WithLabelValues("group_failed", variant).Inc()
			warnings = append(warnings, replicaGroupFailureWarning(group, variant))
			continue
		}

		if selectedLiveIndex != group.live[0] ||
			(len(group.configured) > 0 && live[selectedLiveIndex] != group.configured[0]) {
			metrics.ALBTSMReplicaEvents.WithLabelValues("fallback", variant).Inc()
			logger.Warn("tsm selected fallback replica", logging.Pairs{
				"backend_name":  live[selectedLiveIndex].Name(),
				"replica_group": group.id,
				"variant":       variant,
			})
		}
		if len(usable) > 1 {
			metrics.ALBTSMReplicaEvents.WithLabelValues("suppressed", variant).
				Add(float64(len(usable) - 1))
		}
		conflicts := replicaConflictCount(usable)
		if conflicts > 0 {
			metrics.ALBTSMReplicaEvents.WithLabelValues("conflict", variant).
				Add(float64(conflicts))
			logger.Warn("tsm replica contributions differ", logging.Pairs{
				"backend_name":  live[selectedLiveIndex].Name(),
				"replica_group": group.id,
				"variant":       variant,
				"conflicts":     conflicts,
			})
		}
		logical = append(logical, coalesceReplicaGroup(usable, toleranceNanos))
	}
	return logical, warnings, hasFailure
}

func replicaGroupFailureWarning(group replicaGroup, variant string) string {
	if member, ok := strings.CutPrefix(group.id, "\x00member-"); ok {
		if _, err := strconv.Atoi(member); err == nil {
			if variant != "" {
				return "trickster: tsm excluded pool member " + member +
					": variant \"" + variant + "\" returned no usable response"
			}
			return "trickster: tsm partial failure: pool member " + member +
				" returned no usable response"
		}
	}
	if variant != "" {
		return "trickster: tsm logical replica group " + group.id +
			" returned no usable response for variant \"" + variant + "\""
	}
	return "trickster: tsm logical replica group " + group.id +
		" returned no usable response"
}

// coalesceReplicaGroup uses the configured-first replica as authoritative at
// overlapping points while allowing later replicas to fill missing series and
// timestamps. The resulting contribution retains the provider's original
// merge functions for the subsequent cross-group reduction.
func coalesceReplicaGroup(contributions []*gatherContribution,
	toleranceNanos int64,
) *gatherContribution {
	first := contributions[0]
	if len(contributions) == 1 {
		return first
	}
	dataSets := make([]*dataset.DataSet, 0, len(contributions))
	for _, contribution := range contributions {
		ds, ok := contribution.data.(*dataset.DataSet)
		if !ok || ds == nil {
			return first
		}
		dataSets = append(dataSets, ds)
	}
	var base *dataset.DataSet
	if toleranceNanos > 0 {
		// Tolerant dedup keeps the first point in each timestamp cluster, and
		// dataset merging places existing points before incoming points. Start
		// with the configured-first replica and merge the remaining replicas in
		// pool order so that configured-first remains authoritative.
		base = cloneReplicaDataSet(dataSets[0])
		for i := 1; i < len(dataSets); i++ {
			base.MergeWithStrategyTolerant(true, int(tsmerge.StrategyDedup),
				toleranceNanos, dataSets[i])
		}
	} else {
		// Exact-timestamp dedup is last-value-wins. Merge in reverse pool order
		// so that the configured-first replica is applied last.
		base = cloneReplicaDataSet(dataSets[len(dataSets)-1])
		for i := len(dataSets) - 2; i >= 0; i-- {
			base.MergeWithStrategyTolerant(true, int(tsmerge.StrategyDedup),
				toleranceNanos, dataSets[i])
		}
	}
	return &gatherContribution{
		data:           base,
		mergeFunc:      first.mergeFunc,
		batchMergeFunc: first.batchMergeFunc,
		member:         first.member,
	}
}

func cloneReplicaDataSet(source *dataset.DataSet) *dataset.DataSet {
	clone := source.Clone().(*dataset.DataSet)
	clone.Status = source.Status
	clone.ErrorType = source.ErrorType
	clone.Warnings = append([]string(nil), source.Warnings...)
	return clone
}

func coalesceReplicaResults(live, configured pool.Targets,
	results []gatherResult,
) []gatherResult {
	groups := replicaTopology(live, configured)
	logical := make([]gatherResult, 0, len(groups))
	for _, group := range groups {
		var fallback *gatherResult
		var selected *gatherResult
		for _, liveIndex := range group.live {
			if liveIndex >= len(results) {
				continue
			}
			result := &results[liveIndex]
			if fallback == nil && (result.mergeFunc != nil || result.statusCode != 0) {
				fallback = result
			}
			if !result.failed && result.contrib != nil &&
				result.statusCode >= 200 && result.statusCode < 300 {
				selected = result
				break
			}
		}
		switch {
		case selected != nil:
			logical = append(logical, *selected)
		case fallback != nil:
			failed := *fallback
			failed.failed = true
			logical = append(logical, failed)
		default:
			logical = append(logical, gatherResult{failed: true})
		}
	}
	return logical
}

type replicaPointKey struct {
	statement int
	series    dataset.Hash
	epoch     int64
}

// applyPlanPointCompleteness intersects all variants for each physical member
// before replica selection. This prevents a sum point from one replica from
// being paired with a count point from another replica merely because both
// replicas belong to the same logical group.
func applyPlanPointCompleteness(plan *tsmerge.TSMMergePlan,
	executions []planVariantExecution,
	memberCount int,
) {
	if plan == nil || plan.Completeness != tsmerge.TSMCompletenessAllVariants ||
		len(executions) < 2 {
		return
	}
	for member := range memberCount {
		variantSets := make([]map[replicaPointKey]struct{}, len(executions))
		compatible := true
		for variantIndex := range executions {
			if member >= len(executions[variantIndex].contributions) {
				compatible = false
				break
			}
			contribution := executions[variantIndex].contributions[member]
			if contribution == nil {
				compatible = false
				break
			}
			ds, ok := contribution.data.(*dataset.DataSet)
			if !ok || ds == nil {
				compatible = false
				break
			}
			variantSets[variantIndex] = dataSetPointKeys(ds, plan.OriginalQuery)
		}
		if !compatible {
			for variantIndex := range executions {
				if member < len(executions[variantIndex].contributions) {
					executions[variantIndex].contributions[member] = nil
				}
			}
			continue
		}
		complete := variantSets[0]
		for key := range complete {
			for variantIndex := 1; variantIndex < len(variantSets); variantIndex++ {
				if _, ok := variantSets[variantIndex][key]; !ok {
					delete(complete, key)
					break
				}
			}
		}
		for variantIndex := range executions {
			ds := executions[variantIndex].contributions[member].data.(*dataset.DataSet)
			pruneDataSetPoints(ds, plan.OriginalQuery, complete)
		}
	}
}

func dataSetPointKeys(ds *dataset.DataSet, pairingQuery string) map[replicaPointKey]struct{} {
	keys := make(map[replicaPointKey]struct{})
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			hash := ds.PairingHash(&series.Header, pairingQuery)
			for _, point := range series.Points {
				keys[replicaPointKey{
					statement: result.StatementID,
					series:    hash,
					epoch:     int64(point.Epoch),
				}] = struct{}{}
			}
		}
	}
	return keys
}

func pruneDataSetPoints(ds *dataset.DataSet, pairingQuery string,
	complete map[replicaPointKey]struct{},
) {
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		var seriesCount int
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			hash := ds.PairingHash(&series.Header, pairingQuery)
			var pointCount int
			for _, point := range series.Points {
				key := replicaPointKey{
					statement: result.StatementID,
					series:    hash,
					epoch:     int64(point.Epoch),
				}
				if _, ok := complete[key]; !ok {
					continue
				}
				series.Points[pointCount] = point
				pointCount++
			}
			series.Points = series.Points[:pointCount]
			if pointCount == 0 {
				continue
			}
			result.SeriesList[seriesCount] = series
			seriesCount++
		}
		result.SeriesList = result.SeriesList[:seriesCount]
	}
}

func replicaConflictCount(contributions []*gatherContribution) int {
	points := make(map[replicaPointKey][]any)
	var conflicts int
	for _, contribution := range contributions {
		ds, ok := contribution.data.(*dataset.DataSet)
		if !ok || ds == nil {
			continue
		}
		for _, result := range ds.Results {
			if result == nil {
				continue
			}
			for _, series := range result.SeriesList {
				if series == nil {
					continue
				}
				hash := series.Header.CalculateHash()
				for _, point := range series.Points {
					key := replicaPointKey{
						statement: result.StatementID,
						series:    hash,
						epoch:     int64(point.Epoch),
					}
					if preferred, exists := points[key]; exists {
						if !reflect.DeepEqual(preferred, point.Values) {
							conflicts++
						}
						continue
					}
					points[key] = point.Values
				}
			}
		}
	}
	return conflicts
}
