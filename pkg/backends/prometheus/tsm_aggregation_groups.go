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
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

type aggregationGroups struct {
	grouping promql.AggregationGrouping
	ids      map[string]int
	keyBuf   []byte
}

func (g *aggregationGroups) id(tags dataset.Tags) int {
	// groups are numbered as their first series arrive, so a number past those a caller holds is a new group's;
	// every series shares the one group of an ungrouped aggregation
	if !g.grouping.Without && len(g.grouping.Labels) == 0 {
		return 0
	}
	// the key is built in reused scratch space, and only a group not seen before allocates its key
	g.keyBuf = appendGroupingKey(g.keyBuf[:0], tags, g.grouping)
	if id, ok := g.ids[string(g.keyBuf)]; ok {
		return id
	}
	if g.ids == nil {
		g.ids = make(map[string]int)
	}
	id := len(g.ids)
	g.ids[string(g.keyBuf)] = id
	return id
}

func aggregationHeader(series *dataset.Series, grouping promql.AggregationGrouping, query string) dataset.SeriesHeader {
	tags := aggregationGroupingTags(series.Header.Tags, grouping)
	header := series.Header.Clone()
	header.Tags = tags
	header.Name = tags[promql.MetricNameLabel]
	header.TagFieldsList = nil
	header.QueryStatement = query
	header.CalculateHash(true)
	header.CalculateSize()
	return header
}

func aggregationGroupingTags(tags dataset.Tags, grouping promql.AggregationGrouping) dataset.Tags {
	if grouping.Without {
		output := tags.Clone()
		delete(output, promql.MetricNameLabel)
		for _, label := range grouping.Labels {
			delete(output, label)
		}
		return output
	}
	output := make(dataset.Tags, len(grouping.Labels))
	for _, label := range grouping.Labels {
		if value, found := tags[label]; found && value != "" {
			output[label] = value
		}
	}
	return output
}

func appendGroupingKey(dst []byte, tags dataset.Tags, grouping promql.AggregationGrouping) []byte {
	// appends aggregationGroupingTags(tags, grouping).JSON() without building those tags; parsing leaves
	// grouping's labels sorted and unique, so by-labels are already in JSON key order
	var arr [16]string
	names := arr[:0]
	if grouping.Without {
		for name := range tags {
			if name != promql.MetricNameLabel && !slices.Contains(grouping.Labels, name) {
				names = append(names, name)
			}
		}
		slices.Sort(names)
	} else {
		for _, name := range grouping.Labels {
			if tags[name] != "" {
				names = append(names, name)
			}
		}
	}
	dst = append(dst, '{')
	for i, name := range names {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = tstrings.AppendJSON(dst, name)
		dst = append(dst, ':')
		dst = tstrings.AppendJSON(dst, tags[name])
	}
	return append(dst, '}')
}
