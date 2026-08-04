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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// LimitQueryRange intercepts requests to enforce a maximum query time range if configured.
func LimitQueryRange(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc, ok := tctx.Resources(r.Context()).(*request.Resources)
		if !ok || rsc == nil || rsc.BackendOptions == nil || rsc.BackendClient == nil {
			next.ServeHTTP(w, r)
			return
		}

		limit := time.Duration(rsc.BackendOptions.MaxQueryRange)
		if limit <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		if tsClient, ok := rsc.BackendClient.(backends.TimeseriesBackend); ok {
			trq, _, _, err := tsClient.ParseTimeRangeQuery(r)
			if err == nil && trq != nil {
				duration := trq.Extent.End.Sub(trq.Extent.Start)
				if duration > limit {
					metrics.ProxyQueryRangeRejections.WithLabelValues(rsc.BackendOptions.Name).Inc()
					clientIP := r.Header.Get("X-Forwarded-For")
					if clientIP == "" {
						clientIP = r.RemoteAddr
					}
					logger.Warn("query rejected due to max_query_range limit",
						logging.Pairs{
							"backendName": rsc.BackendOptions.Name,
							"clientIP":    clientIP,
							"path":        r.URL.Path,
							"statement":   trq.Statement,
							"start":       trq.Extent.Start.String(),
							"end":         trq.Extent.End.String(),
							"duration":    duration.String(),
							"limit":       limit.String(),
						})
					http.Error(w, "query time range exceeds the allowed limit of "+limit.String(), http.StatusBadRequest)
					return
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}
