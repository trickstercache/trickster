/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

	"github.com/tricksterproxy/trickster/pkg/cache"
	co "github.com/tricksterproxy/trickster/pkg/cache/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/context"
	"github.com/tricksterproxy/trickster/pkg/backends"
	oo "github.com/tricksterproxy/trickster/pkg/backends/options"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
	"github.com/tricksterproxy/trickster/pkg/tracing"
)

// Resources is a collection of resources a Trickster request would need to fulfill the client request
// This is stored in the client request's context for use by request handers.
type Resources struct {
	BackendOptions      *oo.Options
	PathConfig        *po.Options
	CacheConfig       *co.Options
	NoLock            bool
	CacheClient       cache.Cache
	BackendClient      backends.Client
	AlternateCacheTTL time.Duration
	TimeRangeQuery    *timeseries.TimeRangeQuery
	Tracer            *tracing.Tracer
	Logger            interface{}
}

// Clone returns an exact copy of the subject Resources collection
func (r Resources) Clone() *Resources {
	return &Resources{
		BackendOptions:      r.BackendOptions,
		PathConfig:        r.PathConfig,
		CacheConfig:       r.CacheConfig,
		NoLock:            r.NoLock,
		CacheClient:       r.CacheClient,
		BackendClient:      r.BackendClient,
		AlternateCacheTTL: r.AlternateCacheTTL,
		TimeRangeQuery:    r.TimeRangeQuery,
		Tracer:            r.Tracer,
		Logger:            r.Logger,
	}
}

// NewResources returns a new Resources collection based on the provided inputs
func NewResources(oo *oo.Options, po *po.Options, co *co.Options,
	c cache.Cache, client backends.Client, t *tracing.Tracer,
	logger interface{}) *Resources {
	return &Resources{
		BackendOptions: oo,
		PathConfig:   po,
		CacheConfig:  co,
		CacheClient:  c,
		BackendClient: client,
		Logger:       logger,
		Tracer:       t,
	}
}

// GetResources will return a casted Resource object from the HTTP Request's context
func GetResources(r *http.Request) *Resources {
	if r == nil {
		return nil
	}
	v := context.Resources(r.Context())
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
	return r.WithContext(context.WithResources(r.Context(), rsc))
}
