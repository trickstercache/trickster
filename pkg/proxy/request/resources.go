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

package request

import (
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	auth "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	corso "github.com/trickstercache/trickster/v2/pkg/proxy/cors/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"go.opentelemetry.io/otel/trace"
)

// Resources is a collection of resources a Trickster request would need to fulfill the client request
// This is stored in the client request's context for use by request handers.
type Resources struct {
	sync.Mutex
	BackendOptions    *bo.Options
	PathConfig        *po.Options
	FrontendCORS      *corso.Options
	CacheConfig       *co.Options
	CacheClient       cache.Cache
	BackendClient     backends.Backend
	AlternateCacheTTL time.Duration
	TimeRangeQuery    *timeseries.TimeRangeQuery
	Tracer            *tracing.Tracer
	IsMergeMember     bool
	RequestBody       []byte
	MergeFunc         merge.MergeFunc
	BatchMergeFunc    merge.BatchMergeFunc
	MergeRespondFunc  merge.RespondFunc
	TSUnmarshaler     timeseries.UnmarshalerFunc
	TSMarshaler       timeseries.MarshalWriterFunc
	TSTransformer     func(timeseries.Timeseries)
	TS                timeseries.Timeseries
	TSReqestOptions   *timeseries.RequestOptions
	TSMergeStrategy   int
	// TSDedupToleranceNanos is the tolerance window (in nanoseconds) for
	// clustering near-duplicate samples produced by independent fan-out
	// shards. Zero (default) preserves the legacy exact-epoch dedup behavior.
	TSDedupToleranceNanos int64

	Response       *http.Response
	AuthResult     *auth.AuthResult
	AlreadyEncoded bool
	Cancelable     bool
	// HiddenResult is the X-Trickster-Result value withheld from the client by a path that
	// hides it, kept so the access log can still record the result
	HiddenResult string
	// UpstreamAddr, UpstreamStatus and UpstreamDuration describe the most recent origin
	// exchange made for the request, for the access log
	UpstreamAddr     string
	UpstreamStatus   int
	UpstreamDuration time.Duration
	// SpanContext identifies the request's trace span when tracing is enabled
	SpanContext trace.SpanContext
}

// SetUpstream records an origin exchange; exchanges made concurrently for one
// request (range fan-out) share the resources, so the write is serialized.
func (r *Resources) SetUpstream(addr string, status int, elapsed time.Duration) {
	if r == nil {
		return
	}
	r.Lock()
	r.UpstreamAddr, r.UpstreamStatus, r.UpstreamDuration = addr, status, elapsed
	r.Unlock()
}

// Upstream returns the recorded origin exchange.
func (r *Resources) Upstream() (addr string, status int, elapsed time.Duration) {
	if r == nil {
		return "", 0, 0
	}
	r.Lock()
	defer r.Unlock()
	return r.UpstreamAddr, r.UpstreamStatus, r.UpstreamDuration
}

// Clone returns an exact copy of the subject Resources collection
func (r *Resources) Clone() *Resources {
	r.Lock()
	defer r.Unlock()
	return &Resources{
		BackendOptions:        r.BackendOptions,
		PathConfig:            r.PathConfig,
		FrontendCORS:          r.FrontendCORS,
		CacheConfig:           r.CacheConfig,
		CacheClient:           r.CacheClient,
		BackendClient:         r.BackendClient,
		AlternateCacheTTL:     r.AlternateCacheTTL,
		TimeRangeQuery:        r.TimeRangeQuery,
		Tracer:                r.Tracer,
		IsMergeMember:         r.IsMergeMember,
		RequestBody:           slices.Clone(r.RequestBody),
		MergeFunc:             r.MergeFunc,
		BatchMergeFunc:        r.BatchMergeFunc,
		MergeRespondFunc:      r.MergeRespondFunc,
		TSUnmarshaler:         r.TSUnmarshaler,
		TSMarshaler:           r.TSMarshaler,
		TSTransformer:         r.TSTransformer,
		TS:                    r.TS,
		TSReqestOptions:       r.TSReqestOptions,
		TSMergeStrategy:       r.TSMergeStrategy,
		TSDedupToleranceNanos: r.TSDedupToleranceNanos,
		AuthResult:            r.AuthResult, // shallow copy of the auth result
		AlreadyEncoded:        r.AlreadyEncoded,
		Cancelable:            r.Cancelable,
		HiddenResult:          r.HiddenResult,
		UpstreamAddr:          r.UpstreamAddr,
		UpstreamStatus:        r.UpstreamStatus,
		UpstreamDuration:      r.UpstreamDuration,
		SpanContext:           r.SpanContext,
	}
}

// NewResources returns a new Resources collection based on the provided inputs
func NewResources(oo *bo.Options, pathOpts *po.Options, cacheOpts *co.Options,
	c cache.Cache, client backends.Backend, t *tracing.Tracer,
) *Resources {
	return &Resources{
		BackendOptions: oo,
		PathConfig:     pathOpts,
		CacheConfig:    cacheOpts,
		CacheClient:    c,
		BackendClient:  client,
		Tracer:         t,
	}
}

// GetResources will return a casted Resource object from the HTTP Request's context
func GetResources(r *http.Request) *Resources {
	if r == nil {
		return nil
	}
	v := tctx.Resources(r.Context())
	rsc, ok := v.(*Resources)
	if ok {
		return rsc
	}
	return nil
}

// SetResources will save the Resources collection to the HTTP Request's context
func SetResources(r *http.Request, rsc *Resources) *http.Request {
	if rsc == nil {
		return r
	}
	return r.WithContext(tctx.WithResources(r.Context(), rsc))
}

// ClearResources removes Resources from the HTTP Request's context
func ClearResources(r *http.Request) *http.Request {
	if r == nil {
		return r
	}
	return r.WithContext(tctx.ClearResources(r.Context()))
}

// Merge sets the configuration references in the subject resources to the source's
func (r *Resources) Merge(r2 *Resources) {
	if r == nil || r2 == nil {
		return
	}
	r.BackendOptions = r2.BackendOptions
	r.PathConfig = r2.PathConfig
	if r.FrontendCORS == nil {
		r.FrontendCORS = r2.FrontendCORS
	}
	r.CacheConfig = r2.CacheConfig
	r.CacheClient = r2.CacheClient
	r.BackendClient = r2.BackendClient
	r.AlternateCacheTTL = r2.AlternateCacheTTL
	r.TimeRangeQuery = r2.TimeRangeQuery
	r.Tracer = r2.Tracer
	if r2.AuthResult != nil {
		r.AuthResult = r2.AuthResult
	}

	r.RequestBody = slices.Clone(r2.RequestBody)
	r.IsMergeMember = r.IsMergeMember || r2.IsMergeMember
	r.AlreadyEncoded = r.AlreadyEncoded || r2.AlreadyEncoded
	r.MergeFunc = r2.MergeFunc
	r.BatchMergeFunc = r2.BatchMergeFunc
	r.MergeRespondFunc = r2.MergeRespondFunc
	r.Cancelable = r.Cancelable || r2.Cancelable
}
