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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Resources is a collection of resources a Trickster request would need to fulfill the client request
// This is stored in the client request's context for use by request handers.
type Resources struct {
	BackendOptions    *bo.Options
	PathConfig        *po.Options
	CacheConfig       *co.Options
	NoLock            bool
	CacheClient       cache.Cache
	BackendClient     backends.Backend
	AlternateCacheTTL time.Duration
	TimeRangeQuery    *timeseries.TimeRangeQuery
	Tracer            *tracing.Tracer
	Logger            interface{}
	IsMergeMember     bool
	ResponseBytes     []byte
	ResponseMergeFunc interface{}
	TSUnmarshaler     timeseries.UnmarshalerFunc
	TSMarshaler       timeseries.MarshalWriterFunc
	TSTransformer     func(timeseries.Timeseries)
	TS                timeseries.Timeseries
	TSReqestOptions   *timeseries.RequestOptions
	Response          *http.Response
}

// Clone returns an exact copy of the subject Resources collection
func (r *Resources) Clone() *Resources {
	return &Resources{
		BackendOptions:    r.BackendOptions,
		PathConfig:        r.PathConfig,
		CacheConfig:       r.CacheConfig,
		NoLock:            r.NoLock,
		CacheClient:       r.CacheClient,
		BackendClient:     r.BackendClient,
		AlternateCacheTTL: r.AlternateCacheTTL,
		TimeRangeQuery:    r.TimeRangeQuery,
		Tracer:            r.Tracer,
		Logger:            r.Logger,
		IsMergeMember:     r.IsMergeMember,
		ResponseBytes:     r.ResponseBytes,
		ResponseMergeFunc: r.ResponseMergeFunc,
		TSUnmarshaler:     r.TSUnmarshaler,
		TSMarshaler:       r.TSMarshaler,
		TSTransformer:     r.TSTransformer,
		TS:                r.TS,
		TSReqestOptions:   r.TSReqestOptions,
	}
}

// NewResources returns a new Resources collection based on the provided inputs
func NewResources(oo *bo.Options, po *po.Options, co *co.Options,
	c cache.Cache, client backends.Backend, t *tracing.Tracer,
	logger interface{}) *Resources {
	return &Resources{
		BackendOptions: oo,
		PathConfig:     po,
		CacheConfig:    co,
		CacheClient:    c,
		BackendClient:  client,
		Logger:         logger,
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

// Merge sets the configuration references in the subject resources to the source's
func (r *Resources) Merge(r2 *Resources) {
	if r == nil || r2 == nil {
		return
	}
	r.BackendOptions = r2.BackendOptions
	r.PathConfig = r2.PathConfig
	r.CacheConfig = r2.CacheConfig
	r.NoLock = r2.NoLock
	r.CacheClient = r2.CacheClient
	r.BackendClient = r2.BackendClient
	r.AlternateCacheTTL = r2.AlternateCacheTTL
	r.TimeRangeQuery = r2.TimeRangeQuery
	r.Tracer = r2.Tracer
	r.Logger = r2.Logger
}
