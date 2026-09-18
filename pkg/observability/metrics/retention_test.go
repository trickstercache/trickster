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

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserveTimeseriesRetentionFactor(t *testing.T) {
	tests := []struct {
		name             string
		requestedBuckets int64
		retentionFactor  int
		want             float64
	}{
		// 7 days at a 5m step against the 1024-bucket default
		{"exceeds", 2016, 1024, 1},
		{"fits", 2016, 2048, 0},
		{"exactly at the factor", 1024, 1024, 0},
		{"one over", 1025, 1024, 1},
		{"unlimited", 2016, 0, 0},
		{"negative factor", 2016, -1, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := "retention-" + test.name
			ObserveTimeseriesRetentionFactor(backend, test.requestedBuckets, test.retentionFactor)
			got := testutil.ToFloat64(TimeseriesRetentionFactorExceeded.WithLabelValues(backend))
			if got != test.want {
				t.Errorf("exceeded count = %v, want %v", got, test.want)
			}
		})
	}
}
