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

	"go.opentelemetry.io/otel/api/trace"
)

type currentSpanKeyType struct{}
type ctxAttrType struct{}
type ctxTracerNameType struct{}
type ctxSpanType struct{}

var (
	currentSpanKey = &currentSpanKeyType{}
	attrKey        = &ctxAttrType{}
	tracerNameKey  = &ctxTracerNameType{}
	spanCtxKey     = &ctxSpanType{}
)

func ContextWithSpan(ctx context.Context, span trace.Span) context.Context {
	return context.WithValue(ctx, currentSpanKey, span)
}

func SpanFromContext(ctx context.Context) trace.Span {
	if span, ok := ctx.Value(currentSpanKey).(trace.Span); ok {
		return span
	}
	return trace.NoopSpan{}
}
