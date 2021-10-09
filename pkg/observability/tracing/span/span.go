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

package span

import (
	"context"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"

	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
)

// PrepareRequest extracts trace information from the headers of the incoming request.
// It returns a pointer to the incoming request with the request context updated to include
// all span and tracing info. It also returns a span with the name "Request" that is meant
// to be a parent span for all child spans of this request.
func PrepareRequest(r *http.Request, tr *tracing.Tracer) (*http.Request, trace.Span) {

	if tr == nil || tr.Tracer == nil {
		return r, nil
	}

	if tctx.HealthCheckFlag(r.Context()) {
		return r, nil
	}

	attrs, entries, spanCtx := otelhttptrace.Extract(r.Context(), r)
	attrs = filterAttributes(tr, attrs)

	r = r.WithContext(baggage.ContextWithBaggage(r.Context(), entries))

	// This will add any configured static tags to the span for Zipkin
	// For Jaeger, they are automatically included in the Process section of the Trace
	if tr.Options.AttachTagsToSpan() {
		if len(attrs) > 0 {
			tracing.Tags(tr.Options.Tags).MergeAttr(attrs)
		}
		attrs = tracing.Tags(tr.Options.Tags).ToAttr()
	}

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

	if tctx.HealthCheckFlag(ctx) {
		return ctx, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if tr == nil {
		return ctx, nil
	}

	ctx, span = tr.Start(
		ctx,
		spanName,
	)

	if span != nil && tr.Options.AttachTagsToSpan() {
		span.SetAttributes(tracing.Tags(tr.Options.Tags).ToAttr()...)
	}

	return ctx, span

}

// SetAttributes safely sets attributes on a span, unless they are in the omit list
func SetAttributes(tr *tracing.Tracer, span trace.Span, kvs ...attribute.KeyValue) {
	l := len(kvs)
	if tr == nil || span == nil || l == 0 {
		return
	}
	span.SetAttributes(filterAttributes(tr, kvs)...)
}

func filterAttributes(tr *tracing.Tracer, kvs []attribute.KeyValue) []attribute.KeyValue {
	l := len(kvs)
	if tr == nil || tr.Tracer == nil || l == 0 || tr.Options == nil ||
		len(tr.Options.OmitTagsList) == 0 {
		return kvs
	}
	approved := make([]attribute.KeyValue, 0, l)
	for _, kv := range kvs {
		// if the key is not in the omit list, add it to the approved list
		if _, ok := tr.Options.OmitTags[string(kv.Key)]; !ok {
			approved = append(approved, kv)
		}
	}
	return approved
}
