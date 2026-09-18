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

package metrics

// ObserveTimeseriesRetentionFactor records a request whose bucket count
// exceeds the backend's timeseries_retention_factor. The cache retains only
// the newest retentionFactor buckets, so the cropped remainder is refetched on
// every request and the response can never reach a full cache hit. A
// non-positive retentionFactor disables the check.
func ObserveTimeseriesRetentionFactor(backendName string,
	requestedBuckets int64, retentionFactor int,
) {
	if retentionFactor <= 0 || requestedBuckets <= int64(retentionFactor) {
		return
	}
	TimeseriesRetentionFactorExceeded.WithLabelValues(backendName).Inc()
}
