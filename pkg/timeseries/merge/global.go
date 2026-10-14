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

package merge

import "time"

// Deduplications name how global evaluation combines samples of series that differ only by replica labels
const (
	// TSMDeduplicationPenalty keeps one replica's samples and switches replicas across gaps
	TSMDeduplicationPenalty = "penalty"
	// TSMDeduplicationChain keeps every distinct timestamp of every replica
	TSMDeduplicationChain = "chain"
)

// TSMGlobalSettings configures how an ALB answers each query once over its members' deduplicated raw
// series. Zero limits are unlimited.
type TSMGlobalSettings struct {
	// Name is the ALB's backend name, which labels the evaluation's metrics
	Name                 string
	ReplicaLabels        []string
	Deduplication        string
	PartialResponse      bool
	MaxSamples           int
	QueryTimeout         time.Duration
	MaxConcurrentQueries int
	MaxFetchedSeries     int
	MaxFetchedSamples    int64
	// MaxResponseBytes bounds each member's raw series response and the result an evaluation encodes
	MaxResponseBytes int64
	// RawCacheWindow is how far back from now members' raw series are cached
	RawCacheWindow time.Duration
}
