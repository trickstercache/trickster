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

package span

import (
	"context"
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/tracing"
	"go.opentelemetry.io/otel/api/correlation"
	"go.opentelemetry.io/otel/api/trace"
	"go.opentelemetry.io/otel/plugin/httptrace"
)

// PrepareRequest extracts trace information from the headers of the incoming request.
// It returns a pointer to the incoming request with the request context updated to include
// all span and tracing info. It also returns a span with the name "Request" that is meant
// to be a parent span for all child spans of this request.
func PrepareRequest(r *http.Request, tr trace.Tracer) (*http.Request, trace.Span) {

	attrs, entries, spanCtx := httptrace.Extract(r.Context(), r)

	r = r.WithContext(correlation.ContextWithMap(r.Context(),
		correlation.NewMap(correlation.MapUpdate{
			MultiKV: entries,
		})))

	ctx, span := tr.Start(
		trace.ContextWithRemoteSpanContext(r.Context(), spanCtx),
		"request",
		trace.WithAttributes(attrs...),
	)

	return r.WithContext(ctx), span
}

// NewChildSpan returns the context with a new Span situated as the child of the previous span
func NewChildSpan(ctx context.Context, tr *tracing.Tracer,
	spanName string) (context.Context, trace.Span) {

	var span trace.Span

	if ctx == nil {
		ctx = context.Background()
	}

	if tr == nil {
		return ctx, trace.NoopSpan{}
	}

	ctx, span = tr.Start(
		ctx,
		spanName,
	)

	return ctx, span

}
