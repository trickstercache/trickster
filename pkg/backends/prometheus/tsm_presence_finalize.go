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
	"cmp"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type presenceGroup struct {
	header dataset.SeriesHeader
	series int
	// counts holds the label sets present at each timestamp slot
	counts []int32
}

// presenceIdentity is one inner label set; seen is kept only when members returned it as several series
type presenceIdentity struct {
	group  *presenceGroup
	series int
	seen   []bool
}

// presenceSlots numbers the distinct timestamps of a result. Series of one result usually share their
// timestamps, so each row is first matched against the previous series' timestamp at the same row.
type presenceSlots struct {
	epochs   []epoch.Epoch
	index    map[epoch.Epoch]int
	previous []int
}

func (s *presenceSlots) slot(row int, e epoch.Epoch) int {
	if row < len(s.previous) && s.epochs[s.previous[row]] == e {
		return s.previous[row]
	}
	slot, found := s.index[e]
	if !found {
		slot = len(s.epochs)
		s.epochs = append(s.epochs, e)
		s.index[e] = slot
	}
	if row < len(s.previous) {
		s.previous[row] = slot
	} else {
		s.previous = append(s.previous, slot)
	}
	return slot
}

// finalizePresenceAggregation applies an outer count or group to the union of the inner label sets
// that members returned, counting each label set once per timestamp wherever any member had it
func finalizePresenceAggregation(ds *dataset.DataSet, spec promql.PresenceAggregation) {
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	if dataSetContainsFinalizerInput(ds, spec.Inner) {
		for _, result := range ds.Results {
			if result != nil {
				finalizePresenceResult(result, spec)
			}
		}
	}
	if spec.SortSet {
		sortFinalSeries(ds, spec.SortDescending)
	}
}

func finalizePresenceResult(result *dataset.Result, spec promql.PresenceAggregation) {
	groups := make(map[string]*presenceGroup)
	groupOrder := make([]*presenceGroup, 0)
	identities := make(map[string]*presenceIdentity, len(result.SeriesList))
	seriesIdentities := make([]*presenceIdentity, len(result.SeriesList))
	out := newTextSeriesLog()

	for i, series := range result.SeriesList {
		if series == nil || series.PointCount() == 0 {
			continue
		}
		identityKey := series.Header.Tags.JSON()
		identity := identities[identityKey]
		if identity == nil {
			tags := aggregationGroupingTags(series.Header.Tags, spec.Grouping)
			groupKey := tags.JSON()
			group := groups[groupKey]
			if group == nil {
				header := series.Header.Clone()
				header.Tags = tags
				header.Name = tags[promql.MetricNameLabel]
				header.TagFieldsList = nil
				header.QueryStatement = spec.AggregationQuery
				header.CalculateHash(true)
				header.CalculateSize()
				group = &presenceGroup{header: header, series: out.addSeries()}
				groups[groupKey] = group
				groupOrder = append(groupOrder, group)
			}
			identity = &presenceIdentity{group: group}
			identities[identityKey] = identity
		}
		identity.series++
		seriesIdentities[i] = identity
	}

	slots := presenceSlots{index: make(map[epoch.Epoch]int)}
	for i, series := range result.SeriesList {
		identity := seriesIdentities[i]
		if identity == nil {
			continue
		}
		group := identity.group
		last := -1
		for c := newRowCursor(series.Segments()); !c.done(); c.next() {
			slot := slots.slot(c.n, c.epoch())
			if slot == last {
				continue
			}
			last = slot
			if identity.series > 1 {
				if slot >= len(identity.seen) {
					identity.seen = append(identity.seen, make([]bool, slot+1-len(identity.seen))...)
				}
				if identity.seen[slot] {
					continue
				}
				identity.seen[slot] = true
			}
			if slot >= len(group.counts) {
				group.counts = append(group.counts, make([]int32, slot+1-len(group.counts))...)
			}
			group.counts[slot]++
		}
	}

	order := make([]int, len(slots.epochs))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return cmp.Compare(slots.epochs[a], slots.epochs[b]) })
	for _, group := range groupOrder {
		for _, slot := range order {
			if slot >= len(group.counts) || group.counts[slot] == 0 {
				continue
			}
			value := float64(group.counts[slot])
			if spec.Operator == aggregation.Group {
				value = 1
			}
			out.add(group.series, slots.epochs[slot], value)
		}
	}

	rows := out.finish()
	output := make(dataset.SeriesList, 0, len(groupOrder))
	for _, group := range groupOrder {
		if rows[group.series] == nil {
			continue
		}
		output = append(output, dataset.NewSeriesOf(group.header, rows[group.series]))
	}
	result.SeriesList = output
}
