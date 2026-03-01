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
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"golang.org/x/sync/singleflight"
)

var (
	opcGroup singleflight.Group // deduplicates ObjectProxyCache origin fetches
	dpcGroup singleflight.Group // deduplicates DeltaProxyCache origin fetches
)

// opcResult is the shared result returned to singleflight waiters for OPC.
type opcResult struct {
	statusCode int
	headers    http.Header
	body       []byte
}

// dpcResult is the shared result returned to singleflight waiters for DPC.
// For normal requests, waiters serve wireBody directly (pre-marshaled JSON).
// For IsMergeMember/TSTransformer requests, waiters use rts instead.
type dpcResult struct {
	wireBody           []byte
	rts                timeseries.Timeseries
	headers            http.Header
	statusCode         int
	body               []byte // only populated for error responses
	elapsed            float64
	ffStatus           string
	uncachedValueCount int64
	cacheStatus        status.LookupStatus
	missRanges         timeseries.ExtentList
}
