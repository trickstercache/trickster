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

// WFHistogramSpan represents a span in a native histogram bucket layout.
type WFHistogramSpan struct {
	Offset int32  `json:"offset"`
	Length uint32 `json:"length"`
}

// WFHistogram represents a Prometheus native histogram sample in the JSON
// wire format returned by the Prometheus HTTP API.
type WFHistogram struct {
	Count          string            `json:"count"`
	Sum            string            `json:"sum"`
	Schema         int               `json:"schema"`
	ZeroThreshold  float64           `json:"zero_threshold"`
	ZeroCount      string            `json:"zero_count"`
	NegativeSpans  []WFHistogramSpan `json:"negative_spans,omitempty"`
	NegativeDeltas []int64           `json:"negative_deltas,omitempty"`
	NegativeCounts []string          `json:"negative_counts,omitempty"`
	PositiveSpans  []WFHistogramSpan `json:"positive_spans,omitempty"`
	PositiveDeltas []int64           `json:"positive_deltas,omitempty"`
	PositiveCounts []string          `json:"positive_counts,omitempty"`
	Buckets        [][]any           `json:"buckets,omitempty"`
	CustomValues   []float64         `json:"custom_values,omitempty"`
}
