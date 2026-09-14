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

package promql

import (
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
)

const (
	promMetricNameLabel     = "__name__"
	promMetricTypeLabel     = "__type__"
	promMetricUnitLabel     = "__unit__"
	varianceTemporaryMetric = "__trickster_tsm_variance__"
)

var varianceMetadataLabels = map[string]string{
	promMetricNameLabel: "__trickster_tsm_name__",
	promMetricTypeLabel: "__trickster_tsm_type__",
	promMetricUnitLabel: "__trickster_tsm_unit__",
}

// VarianceAggregation describes an outer stddev or stdvar aggregation,
// optionally wrapped in sort or sort_desc.
type VarianceAggregation struct {
	Operator         string
	Inner            Expr
	AggregationQuery string
	Grouping         AggregationGrouping
	SortSet          bool
	SortDescending   bool

	inputPrefix string
	inputSuffix string
}

// ParseVarianceAggregation parses a complete outer stddev or stdvar
// aggregation, optionally wrapped in sort or sort_desc.
func ParseVarianceAggregation(e Expr) (VarianceAggregation, bool) {
	spec, found := parseOuterAggregation(e, aggregation.StdDev, aggregation.StdVar)
	if !found || len(spec.inputPrefix) < len(spec.Operator) {
		return VarianceAggregation{}, false
	}
	return VarianceAggregation{
		Operator:         spec.Operator,
		Inner:            spec.Inner,
		AggregationQuery: spec.Aggregation.String(),
		Grouping:         spec.Grouping,
		SortSet:          spec.SortSet,
		SortDescending:   spec.SortDescending,
		inputPrefix:      spec.inputPrefix,
		inputSuffix:      spec.inputSuffix,
	}, true
}

// VarianceVariantQuery rewrites spec as one count, avg, or stdvar query. The
// generated input is float-only because those three operators do not otherwise
// share stddev/stdvar's native-histogram behavior.
func VarianceVariantQuery(spec VarianceAggregation, operator string) string {
	metadataLabels := varianceGroupingMetadataLabels(spec.Grouping)
	if len(metadataLabels) == 0 {
		prefix := operator + spec.inputPrefix[len(spec.Operator):]
		return prefix + "clamp(" + spec.Inner.String() + ", -Inf, +Inf)" + spec.inputSuffix
	}

	input := spec.Inner.String()
	internalGrouping := AggregationGrouping{Labels: append([]string(nil), spec.Grouping.Labels...)}
	internalGrouping.Without = spec.Grouping.Without
	finalGrouping := AggregationGrouping{
		Labels:  append([]string(nil), spec.Grouping.Labels...),
		Without: spec.Grouping.Without,
	}
	temporaryLabels := varianceTemporaryLabels(spec.Grouping, metadataLabels)
	for _, label := range metadataLabels {
		temporary := temporaryLabels[label]
		input = formatLabelReplace(input, temporary, "$1", label, "(.*)")
		if spec.Grouping.Without {
			finalGrouping.Labels = append(finalGrouping.Labels, temporary)
		} else {
			for i := range internalGrouping.Labels {
				if internalGrouping.Labels[i] == label {
					internalGrouping.Labels[i] = temporary
				}
			}
		}
	}
	input = "clamp(" + input + ", -Inf, +Inf)"
	result := formatAggregation(operator, internalGrouping, input)
	for _, label := range metadataLabels {
		temporary := temporaryLabels[label]
		result = formatLabelReplace(result, label, "$1", temporary, "(.*)")
	}
	if !slices.Contains(metadataLabels, promMetricNameLabel) {
		// Restoring __name__ clears Prometheus' delayed metadata-drop marker.
		// When only __type__/__unit__ are retained, use a temporary metric name
		// and let the final aggregation remove it again.
		result = formatLabelReplace(result, promMetricNameLabel, varianceTemporaryMetric,
			promMetricNameLabel, ".*")
	}
	return formatAggregation(aggregation.Sum, finalGrouping, result)
}

func formatLabelReplace(input, destination, replacement, source, expression string) string {
	return "label_replace(" + input + ", " + strconv.Quote(destination) + ", " +
		strconv.Quote(replacement) + ", " + strconv.Quote(source) + ", " +
		strconv.Quote(expression) + ")"
}

func varianceTemporaryLabels(grouping AggregationGrouping, metadataLabels []string) map[string]string {
	used := make(map[string]struct{}, len(grouping.Labels)+len(metadataLabels))
	for _, label := range grouping.Labels {
		used[label] = struct{}{}
	}
	temporaryLabels := make(map[string]string, len(metadataLabels))
	for _, label := range metadataLabels {
		candidate := varianceMetadataLabels[label]
		for {
			if _, exists := used[candidate]; !exists {
				break
			}
			candidate += "_"
		}
		temporaryLabels[label] = candidate
		used[candidate] = struct{}{}
	}
	return temporaryLabels
}

func varianceGroupingMetadataLabels(grouping AggregationGrouping) []string {
	if grouping.Without {
		excluded := make(map[string]struct{}, len(grouping.Labels))
		for _, label := range grouping.Labels {
			excluded[label] = struct{}{}
		}
		labels := make([]string, 0, 2)
		for _, label := range []string{promMetricTypeLabel, promMetricUnitLabel} {
			if _, found := excluded[label]; !found {
				labels = append(labels, label)
			}
		}
		return labels
	}
	labels := make([]string, 0, len(varianceMetadataLabels))
	for _, label := range grouping.Labels {
		if _, ok := varianceMetadataLabels[label]; ok {
			labels = append(labels, label)
		}
	}
	return labels
}

func formatAggregation(operator string, grouping AggregationGrouping, input string) string {
	if grouping.Without {
		return operator + " without (" + strings.Join(grouping.Labels, ", ") + ") (" + input + ")"
	}
	if len(grouping.Labels) > 0 {
		return operator + " by (" + strings.Join(grouping.Labels, ", ") + ") (" + input + ")"
	}
	return operator + "(" + input + ")"
}
