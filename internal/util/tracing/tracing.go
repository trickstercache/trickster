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

package tracing

import (
	"context"
	"net/http"

	"github.com/Comcast/trickster/internal/config"

	"go.opentelemetry.io/otel/api/distributedcontext"
	"go.opentelemetry.io/otel/api/trace"
	"go.opentelemetry.io/otel/plugin/httptrace"
)

const (
	serviceName      = "trickster"
	tracestateHeader = "tracestate"
)

// PrepareRequest extracts trace information from the headers of the incoming request. It returns a pointer to the incoming request with the request context updated to include all span and tracing info. It also returns a span with the name "Request" that is meant to be a parent span for all child spans of this request.
func PrepareRequest(r *http.Request, pc *config.PathConfig) (*http.Request, trace.Span) {

	if pc == nil || pc.OriginConfig == nil || pc.OriginConfig.Tracer == nil {
		return r, nil
	}

	tr := pc.OriginConfig.Tracer

	attrs, entries, spanCtx := httptrace.Extract(r.Context(), r)

	ctx := distributedcontext.WithMap(
		r.Context(),
		distributedcontext.NewMap(
			distributedcontext.MapUpdate{
				MultiKV: entries,
			},
		),
	)
	ctx = context.WithValue(ctx, spanCtxKey, spanCtx)
	ctx = context.WithValue(ctx, attrKey, attrs)

	ctx, span := tr.Start(
		ctx,
		"Request",
		trace.WithAttributes(attrs...),
		trace.ChildOf(spanCtx),
	)

	return r.WithContext(ctx), span
}
