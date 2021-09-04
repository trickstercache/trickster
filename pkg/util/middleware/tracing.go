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
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"go.opentelemetry.io/otel/attribute"
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
					[]attribute.KeyValue{
						attribute.String("backend.name", rsc.BackendOptions.Name),
						attribute.String("backend.provider", rsc.BackendOptions.Provider),
						attribute.String("router.path", rsc.PathConfig.Path),
						attribute.String("cache.name", rsc.CacheConfig.Name),
						attribute.String("cache.provider", rsc.CacheConfig.Provider),
					}...,
				)
			}

		}
		next.ServeHTTP(w, r)
	})
}
