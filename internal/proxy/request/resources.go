/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/context"
	"github.com/Comcast/trickster/internal/proxy/origins"
	"github.com/Comcast/trickster/internal/timeseries"
)

// Resources is a collection of resources a Trickster request would need to fulfill the client request
// This is stored in the client request's context for use by request handers.
type Resources struct {
	OriginConfig      *config.OriginConfig
	PathConfig        *config.PathConfig
	CacheConfig       *config.CachingConfig
	NoLock            bool
	CacheClient       cache.Cache
	OriginClient      origins.Client
	AlternateCacheTTL time.Duration
	TimeRangeQuery    *timeseries.TimeRangeQuery
}

// Clone returns an exact copy of the subject Resources collection
func (r Resources) Clone() *Resources {
	return &Resources{
		OriginConfig:      r.OriginConfig,
		PathConfig:        r.PathConfig,
		CacheConfig:       r.CacheConfig,
		NoLock:            r.NoLock,
		CacheClient:       r.CacheClient,
		OriginClient:      r.OriginClient,
		AlternateCacheTTL: r.AlternateCacheTTL,
		TimeRangeQuery:    r.TimeRangeQuery,
	}
}

// NewResources returns a new Resources collection based on the provided inputs
func NewResources(oc *config.OriginConfig, pc *config.PathConfig, cc *config.CachingConfig, c cache.Cache, client origins.Client) *Resources {
	return &Resources{
		OriginConfig: oc,
		PathConfig:   pc,
		CacheConfig:  cc,
		CacheClient:  c,
		OriginClient: client,
	}
}

// GetResources will return a casted Resource object from the HTTP Request's context
func GetResources(r *http.Request) *Resources {
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
