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

package engines

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func BenchmarkDeltaProxyCache(b *testing.B) {
	ts, _, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		b.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	o.FastForwardDisable = true
	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	for i := 0; i < b.N; i++ {
		client.QueryRangeHandler(w, r)
	}
}

func BenchmarkDeltaProxyCacheChunks(b *testing.B) {
	ts, _, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		b.Error(err)
	}
	rsc.CacheConfig.UseCacheChunking = true
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	o.FastForwardDisable = true
	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	for i := 0; i < b.N; i++ {
		client.QueryRangeHandler(w, r)
	}
}
