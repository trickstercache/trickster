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

package tracing

import (
	"context"

	"go.opentelemetry.io/otel/api/core"
	"go.opentelemetry.io/otel/api/trace"
)

// NewChildSpan returns the context with a new Span situated as the child of the previous span
func NewChildSpan(ctx context.Context, tr trace.Tracer, spanName string) (context.Context, trace.Span) {

	if tr == nil {
		return ctx, trace.NoopSpan{}
	}

	if ctx == nil {
		ctx = context.Background()
	}

	attrs, ok := ctx.Value(attrKey).([]core.KeyValue)
	if !ok {
		attrs = make([]core.KeyValue, 0)
	}
	spanCtx, ok := ctx.Value(spanCtxKey).(core.SpanContext)
	if !ok {
		return ctx, trace.NoopSpan{}
	}

	ctx, span := tr.Start(
		ctx,
		spanName,
		trace.WithAttributes(attrs...),
		trace.ChildOf(spanCtx),
	)

	return ctx, span

}
