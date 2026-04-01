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

package backends

import "net/http"

// TSMMergeProvider is an optional interface that a Backend may implement to
// participate in Time Series Merge (TSM) scatter/gather with query-aware merge
// strategies.
//
// If a backend does not implement TSMMergeProvider, TSM falls back to
// deduplication for all queries.
type TSMMergeProvider interface {
	// ClassifyMerge inspects the query string and returns:
	//   - strategy: the merge strategy to use for fanout accumulation, as the
	//     underlying int value of dataset.MergeStrategy (avoids importing the
	//     dataset package here and breaking the shallow backends import graph).
	//   - needsDualQuery: true when a weighted arithmetic mean is required
	//     (i.e. the outer aggregator is "avg"). TSM will fire two sub-queries —
	//     a "sum" variant and a "count" variant — and divide after gathering.
	//   - warning: non-empty when the aggregator cannot be correctly merged
	//     across fanout shards (e.g. stddev, quantile). TSM will fall back to
	//     deduplication and surface this warning in the response.
	ClassifyMerge(query string) (strategy int, needsDualQuery bool, warning string)

	// RewriteForWeightedAvg returns two modified copies of r — one with the
	// outer aggregator replaced by its "sum" equivalent and one by its "count"
	// equivalent. Both copies have the replacement injected in whatever way the
	// provider's wire protocol requires (URL query parameter, POST body, header,
	// etc.). The returned requests must be safe for concurrent use; they should
	// not share any mutable state with each other or with r.
	RewriteForWeightedAvg(r *http.Request, query string) (sumReq, countReq *http.Request)
}
