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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/api/trace"
)

type ctxOption func(context.Context) context.Context

func makeCTX(options ...ctxOption) context.Context {
	ctx := context.Background()

	for _, f := range options {
		ctx = f(ctx)

	}
	return ctx
}

func TestSpanFromContext(t *testing.T) {
	ctx := makeCTX()
	span := SpanFromContext(ctx)
	span.AddEvent(ctx, "testMessage")
	_, ok := span.(trace.NoopSpan)
	if !ok {
		assert.Fail(t, "Wrong type for Span. Expected NoopSpan, go", reflect.TypeOf(span))
	}
}

func TestContextWithSpan(t *testing.T) {
	span := SpanFromContext(context.Background())
	ctx := makeCTX(func(ctx context.Context) context.Context {
		return context.WithValue(ctx, TracerImplementation(1), "value")
	})
	newctx := ContextWithSpan(ctx, span)
	newSpan := SpanFromContext(newctx)
	assert.Equal(t, span, newSpan, "Spans are not the same span")
	assert.NotEqual(t, ctx, newctx, "Contexts are the same object")

}
