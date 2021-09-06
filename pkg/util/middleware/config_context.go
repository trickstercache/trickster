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

package middleware

import (
	"math/rand"
	"net/http"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/context"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// WithResourcesContext ...
func WithResourcesContext(client backends.Backend, o *bo.Options,
	c cache.Cache, p *po.Options, t *tracing.Tracer,
	l interface{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if o != nil && (o.LatencyMinMS > 0 || o.LatencyMaxMS > 0) {
			processSimulatedLatency(w, o.LatencyMinMS, o.LatencyMaxMS)
		}

		var resources *request.Resources
		if c == nil {
			resources = request.NewResources(o, p, nil, nil, client, t, l)
		} else {
			resources = request.NewResources(o, p, c.Configuration(), c, client, t, l)
		}
		ctx := r.Context()
		rsc, ok := context.Resources(ctx).(*request.Resources)
		if !ok {
			next.ServeHTTP(w, r.WithContext(context.WithResources(r.Context(), resources)))
			return
		}
		rsc.Merge(resources)

		next.ServeHTTP(w, r.WithContext(context.WithResources(r.Context(), rsc)))
	})
}
