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

package middleware

import (
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/observability/tracing"
	tspan "github.com/tricksterproxy/trickster/pkg/observability/tracing/span"
	"github.com/tricksterproxy/trickster/pkg/proxy/request"

	"go.opentelemetry.io/otel/label"
)

// Trace attaches a Tracer to an HTTP request
func Trace(tr *tracing.Tracer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		r, span := tspan.PrepareRequest(r, tr)
		if span != nil {
			defer span.End()

			rsc := request.GetResources(r)
			if rsc != nil &&
				rsc.BackendOptions != nil &&
				rsc.PathConfig != nil &&
				rsc.CacheConfig != nil {
				tspan.SetAttributes(tr, span,
					[]label.KeyValue{
						label.String("backend.name", rsc.BackendOptions.Name),
						label.String("backend.provider", rsc.BackendOptions.Provider),
						label.String("router.path", rsc.PathConfig.Path),
						label.String("cache.name", rsc.CacheConfig.Name),
						label.String("cache.provider", rsc.CacheConfig.Provider),
					}...,
				)
			}

		}
		next.ServeHTTP(w, r)
	})
}
