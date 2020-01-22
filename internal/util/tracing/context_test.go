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
