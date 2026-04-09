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
	"bytes"
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"golang.org/x/sync/singleflight"
)

// sfResponseCapture tees body writes into a buffer so the singleflight result
// captures the response even for non-cacheable status codes (e.g. 502).
// It implements http.ResponseWriter; when the inner writer is not an
// http.ResponseWriter, Header/WriteHeader are safe no-ops.
type sfResponseCapture struct {
	inner io.Writer
	buf   bytes.Buffer
}

func (c *sfResponseCapture) Write(p []byte) (int, error) {
	c.buf.Write(p)
	return c.inner.Write(p)
}

func (c *sfResponseCapture) Header() http.Header {
	if rw, ok := c.inner.(http.ResponseWriter); ok {
		return rw.Header()
	}
	return http.Header{}
}

func (c *sfResponseCapture) WriteHeader(statusCode int) {
	if rw, ok := c.inner.(http.ResponseWriter); ok {
		rw.WriteHeader(statusCode)
	}
}

var (
	opcGroup singleflight.Group // deduplicates ObjectProxyCache origin fetches
	dpcGroup singleflight.Group // deduplicates DeltaProxyCache origin fetches
)

// opcResult is the shared result returned to singleflight waiters for OPC.
type opcResult struct {
	statusCode  int
	headers     http.Header
	body        []byte
	elapsed     float64
	cacheStatus status.LookupStatus
}

// dpcResult is the shared result returned to singleflight waiters for DPC.
// Normal waiters serve wireBody directly; IsMergeMember/TSTransformer waiters use rts.
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
