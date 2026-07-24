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
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

const mixedFloatHistogramWarning = "PromQL warning: encountered a mix of histograms and floats for aggregation"

var prometheusValueOperations dataset.ValueMergeOperations = prometheusHistogramOperations{}

type prometheusHistogramOperations struct{}

type normalizedHistogram struct {
	count   float64
	sum     float64
	buckets []normalizedHistogramBucket
}

type normalizedHistogramBucket struct {
	lower          float64
	upper          float64
	count          float64
	lowerInclusive bool
	upperInclusive bool
}

func (prometheusHistogramOperations) MergeValues(dst, src any,
	strategy merge.Strategy,
) (any, bool) {
	if strategy != merge.StrategySum && strategy != merge.StrategyAvg {
		return nil, false
	}
	left, err := parseNormalizedHistogram(dst)
	if err != nil {
		return nil, false
	}
	right, err := parseNormalizedHistogram(src)
	if err != nil {
		return nil, false
	}
	left.count += right.count
	left.sum += right.sum
	left.buckets = coarsenHistogramBuckets(left.buckets, right.buckets)
	value, err := marshalNormalizedHistogram(left)
	return value, err == nil
}

func (prometheusHistogramOperations) DivideValue(value any, divisor float64) (any, bool) {
	if divisor == 0 {
		return nil, false
	}
	histogram, err := parseNormalizedHistogram(value)
	if err != nil {
		return nil, false
	}
	histogram.count /= divisor
	histogram.sum /= divisor
	for i := range histogram.buckets {
		histogram.buckets[i].count /= divisor
	}
	value, err = marshalNormalizedHistogram(histogram)
	return value, err == nil
}

func (prometheusHistogramOperations) PairingHash(header *dataset.SeriesHeader,
	queryStatement string,
) dataset.Hash {
	clone := header.Clone()
	clone.ValueFieldsList = nil
	if queryStatement == "" {
		queryStatement = clone.QueryStatement
	}
	return clone.CalculateHashWithQueryStatement(queryStatement)
}

func (ops prometheusHistogramOperations) FinalizeMerge(ds *dataset.DataSet,
	strategy merge.Strategy,
) {
	if strategy != merge.StrategySum && strategy != merge.StrategyAvg {
		return
	}
	mixedSamples := 0
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		type sampleGroup struct {
			floats     []*dataset.Series
			histograms []*dataset.Series
		}
		groups := make(map[dataset.Hash]*sampleGroup, len(result.SeriesList))
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			hash := ops.PairingHash(&series.Header, "")
			group := groups[hash]
			if group == nil {
				group = &sampleGroup{}
				groups[hash] = group
			}
			if isHistogramSeries(series) {
				group.histograms = append(group.histograms, series)
			} else {
				group.floats = append(group.floats, series)
			}
		}
		for _, group := range groups {
			if len(group.floats) == 0 || len(group.histograms) == 0 {
				continue
			}
			sampleTypes := make(map[epoch.Epoch]uint8)
			for _, series := range group.floats {
				for _, point := range series.Points {
					sampleTypes[point.Epoch] |= 1
				}
			}
			for _, series := range group.histograms {
				for _, point := range series.Points {
					sampleTypes[point.Epoch] |= 2
				}
			}
			mixedEpochs := make(map[epoch.Epoch]struct{})
			for sampleEpoch, sampleType := range sampleTypes {
				if sampleType == 3 {
					mixedEpochs[sampleEpoch] = struct{}{}
				}
			}
			if len(mixedEpochs) == 0 {
				continue
			}
			mixedSamples += len(mixedEpochs)
			for _, series := range append(group.floats, group.histograms...) {
				kept := series.Points[:0]
				for _, point := range series.Points {
					if _, mixed := mixedEpochs[point.Epoch]; !mixed {
						kept = append(kept, point)
					}
				}
				series.Points = kept
				series.PointSize = kept.Size()
			}
		}
		kept := result.SeriesList[:0]
		for _, series := range result.SeriesList {
			if series != nil && len(series.Points) > 0 {
				kept = append(kept, series)
			}
		}
		result.SeriesList = kept
	}
	if mixedSamples > 0 && !slices.Contains(ds.Warnings, mixedFloatHistogramWarning) {
		ds.Warnings = append(ds.Warnings, mixedFloatHistogramWarning)
	}
}

func isHistogramSeries(series *dataset.Series) bool {
	return len(series.Header.ValueFieldsList) == 1 &&
		series.Header.ValueFieldsList[0].Name == fieldNameHistogram
}

func parseNormalizedHistogram(value any) (normalizedHistogram, error) {
	raw, ok := value.(string)
	if !ok {
		return normalizedHistogram{}, fmt.Errorf("histogram value has type %T", value)
	}
	var wire WFHistogram
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return normalizedHistogram{}, err
	}
	count, err := parseHistogramNumber(wire.Count, false)
	if err != nil {
		return normalizedHistogram{}, fmt.Errorf("parse histogram count: %w", err)
	}
	sum, err := parseHistogramNumber(wire.Sum, false)
	if err != nil {
		return normalizedHistogram{}, fmt.Errorf("parse histogram sum: %w", err)
	}
	out := normalizedHistogram{count: count, sum: sum}
	if len(wire.Buckets) > 0 {
		out.buckets, err = parseExplicitHistogramBuckets(wire.Buckets)
		if err != nil {
			return normalizedHistogram{}, err
		}
		return out, nil
	}
	zeroCount, err := parseHistogramNumber(wire.ZeroCount, true)
	if err != nil {
		return normalizedHistogram{}, fmt.Errorf("parse histogram zero count: %w", err)
	}
	if zeroCount != 0 {
		out.buckets = append(out.buckets, normalizedHistogramBucket{
			lower:          -wire.ZeroThreshold,
			upper:          wire.ZeroThreshold,
			count:          zeroCount,
			lowerInclusive: true,
			upperInclusive: true,
		})
	}
	if err := appendSpanHistogramBuckets(&out.buckets, wire.NegativeSpans,
		wire.NegativeDeltas, wire.NegativeCounts, false, wire.Schema,
		wire.CustomValues); err != nil {
		return normalizedHistogram{}, fmt.Errorf("parse negative histogram buckets: %w", err)
	}
	if err := appendSpanHistogramBuckets(&out.buckets, wire.PositiveSpans,
		wire.PositiveDeltas, wire.PositiveCounts, true, wire.Schema,
		wire.CustomValues); err != nil {
		return normalizedHistogram{}, fmt.Errorf("parse positive histogram buckets: %w", err)
	}
	for i := range out.buckets {
		switch {
		case out.buckets[i].lower > 0 &&
			out.buckets[i].lower < wire.ZeroThreshold:
			out.buckets[i].lower = wire.ZeroThreshold
		case out.buckets[i].upper < 0 &&
			out.buckets[i].upper > -wire.ZeroThreshold:
			out.buckets[i].upper = -wire.ZeroThreshold
		}
	}
	slices.SortFunc(out.buckets, compareHistogramBuckets)
	return out, nil
}

func parseExplicitHistogramBuckets(wireBuckets [][]any) ([]normalizedHistogramBucket, error) {
	buckets := make([]normalizedHistogramBucket, 0, len(wireBuckets))
	for _, wireBucket := range wireBuckets {
		if len(wireBucket) != 4 {
			return nil, fmt.Errorf("histogram bucket has %d fields", len(wireBucket))
		}
		boundaries, err := histogramNumber(wireBucket[0])
		if err != nil || boundaries != math.Trunc(boundaries) || boundaries < 0 || boundaries > 3 {
			return nil, fmt.Errorf("invalid histogram bucket boundaries %v", wireBucket[0])
		}
		lower, err := histogramNumber(wireBucket[1])
		if err != nil {
			return nil, fmt.Errorf("parse histogram lower bound: %w", err)
		}
		upper, err := histogramNumber(wireBucket[2])
		if err != nil {
			return nil, fmt.Errorf("parse histogram upper bound: %w", err)
		}
		count, err := histogramNumber(wireBucket[3])
		if err != nil {
			return nil, fmt.Errorf("parse histogram bucket count: %w", err)
		}
		if math.IsNaN(lower) || math.IsNaN(upper) || lower > upper {
			return nil, fmt.Errorf("invalid histogram bucket interval %v", wireBucket)
		}
		mode := int(boundaries)
		buckets = append(buckets, normalizedHistogramBucket{
			lower:          lower,
			upper:          upper,
			count:          count,
			lowerInclusive: mode == 1 || mode == 3,
			upperInclusive: mode == 0 || mode == 3,
		})
	}
	slices.SortFunc(buckets, compareHistogramBuckets)
	return buckets, nil
}

func appendSpanHistogramBuckets(dst *[]normalizedHistogramBucket,
	spans []WFHistogramSpan, deltas []int64, counts []string, positive bool,
	schema int, customValues []float64,
) error {
	bucketCount := 0
	for _, span := range spans {
		bucketCount += int(span.Length)
	}
	if bucketCount == 0 {
		return nil
	}
	usingCounts := len(counts) > 0
	if usingCounts {
		if len(counts) != bucketCount {
			return fmt.Errorf("span count %d does not match bucket count %d",
				len(counts), bucketCount)
		}
	} else if len(deltas) != bucketCount {
		return fmt.Errorf("span delta count %d does not match bucket count %d",
			len(deltas), bucketCount)
	}

	var index int32
	var absoluteCount float64
	position := 0
	for _, span := range spans {
		index += span.Offset
		for range span.Length {
			var count float64
			var err error
			if usingCounts {
				count, err = parseHistogramNumber(counts[position], false)
				if err != nil {
					return err
				}
			} else {
				absoluteCount += float64(deltas[position])
				count = absoluteCount
			}
			if count != 0 {
				bucket, err := spanHistogramBucket(index, count, positive,
					schema, customValues)
				if err != nil {
					return err
				}
				*dst = append(*dst, bucket)
			}
			index++
			position++
		}
	}
	return nil
}

func spanHistogramBucket(index int32, count float64, positive bool,
	schema int, customValues []float64,
) (normalizedHistogramBucket, error) {
	if schema != -53 && (schema < -4 || schema > 8) {
		return normalizedHistogramBucket{}, fmt.Errorf("invalid histogram schema %d", schema)
	}
	lowerBound, err := histogramBucketBound(index-1, schema, customValues)
	if err != nil {
		return normalizedHistogramBucket{}, err
	}
	upperBound, err := histogramBucketBound(index, schema, customValues)
	if err != nil {
		return normalizedHistogramBucket{}, err
	}
	if positive {
		return normalizedHistogramBucket{
			lower:          lowerBound,
			upper:          upperBound,
			count:          count,
			lowerInclusive: schema == -53 && index == 0,
			upperInclusive: true,
		}, nil
	}
	return normalizedHistogramBucket{
		lower:          -upperBound,
		upper:          -lowerBound,
		count:          count,
		lowerInclusive: true,
		upperInclusive: false,
	}, nil
}

func histogramBucketBound(index int32, schema int, customValues []float64) (float64, error) {
	if schema == -53 {
		index64 := int64(index)
		customValuesLength := int64(len(customValues))
		switch {
		case index == -1:
			return math.Inf(-1), nil
		case index64 == customValuesLength:
			return math.Inf(1), nil
		case index < -1 || index64 > customValuesLength:
			return 0, fmt.Errorf("custom histogram bucket index %d out of range", index)
		default:
			return customValues[index], nil
		}
	}
	if schema < 0 {
		exponent := int(index) << -schema
		if exponent == 1024 {
			return math.MaxFloat64, nil
		}
		return math.Ldexp(1, exponent), nil
	}
	fractionMask := int32(1<<schema) - 1
	if index&fractionMask == 0 && (int(index)>>schema)+1 == 1025 {
		return math.MaxFloat64, nil
	}
	return math.Exp2(float64(index) * math.Exp2(float64(-schema))), nil
}

func coarsenHistogramBuckets(left, right []normalizedHistogramBucket) []normalizedHistogramBucket {
	buckets := make([]normalizedHistogramBucket, 0, len(left)+len(right))
	buckets = append(buckets, left...)
	buckets = append(buckets, right...)
	if len(buckets) < 2 {
		return buckets
	}
	slices.SortFunc(buckets, compareHistogramBuckets)
	// The HTTP API omits empty buckets. Merging overlapping intervals into
	// connected components produces a common layout without splitting any
	// source bucket or inventing a distribution within it.
	merged := buckets[:1]
	for _, next := range buckets[1:] {
		current := &merged[len(merged)-1]
		if histogramBucketsOverlap(*current, next) {
			current.count += next.count
			if next.lower == current.lower {
				current.lowerInclusive = current.lowerInclusive || next.lowerInclusive
			}
			if next.upper > current.upper {
				current.upper = next.upper
				current.upperInclusive = next.upperInclusive
			} else if next.upper == current.upper {
				current.upperInclusive = current.upperInclusive || next.upperInclusive
			}
			continue
		}
		merged = append(merged, next)
	}
	return merged
}

func compareHistogramBuckets(left, right normalizedHistogramBucket) int {
	switch {
	case left.lower < right.lower:
		return -1
	case left.lower > right.lower:
		return 1
	case left.upper < right.upper:
		return -1
	case left.upper > right.upper:
		return 1
	default:
		return 0
	}
}

func histogramBucketsOverlap(left, right normalizedHistogramBucket) bool {
	return right.lower < left.upper ||
		(right.lower == left.upper && left.upperInclusive && right.lowerInclusive)
}

func marshalNormalizedHistogram(histogram normalizedHistogram) (string, error) {
	wire := struct {
		Count   string  `json:"count"`
		Sum     string  `json:"sum"`
		Buckets [][]any `json:"buckets,omitempty"`
	}{
		Count: formatHistogramNumber(histogram.count),
		Sum:   formatHistogramNumber(histogram.sum),
	}
	for _, bucket := range histogram.buckets {
		if bucket.count == 0 {
			continue
		}
		boundaries := 2
		switch {
		case bucket.lowerInclusive && bucket.upperInclusive:
			boundaries = 3
		case bucket.lowerInclusive:
			boundaries = 1
		case bucket.upperInclusive:
			boundaries = 0
		}
		wire.Buckets = append(wire.Buckets, []any{
			boundaries,
			formatHistogramNumber(bucket.lower),
			formatHistogramNumber(bucket.upper),
			formatHistogramNumber(bucket.count),
		})
	}
	data, err := json.Marshal(wire)
	return string(data), err
}

func parseHistogramNumber(value string, optional bool) (float64, error) {
	if value == "" && optional {
		return 0, nil
	}
	return strconv.ParseFloat(value, 64)
}

func histogramNumber(value any) (float64, error) {
	switch typed := value.(type) {
	case string:
		return strconv.ParseFloat(typed, 64)
	case float64:
		return typed, nil
	case float32:
		return float64(typed), nil
	case int:
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case json.Number:
		return typed.Float64()
	default:
		return 0, fmt.Errorf("value has type %T", value)
	}
}

func formatHistogramNumber(value float64) string {
	format := byte('f')
	absolute := math.Abs(value)
	if absolute != 0 && (absolute < 1e-6 || absolute >= 1e21) {
		format = 'e'
	}
	return strconv.FormatFloat(value, format, -1, 64)
}
