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

package testutil

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	to "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// NewRecordingTracer returns a tracer and span recorder for assertions.
func NewRecordingTracer(t testing.TB) (*tracing.Tracer, *tracetest.SpanRecorder) {
	t.Helper()

	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})

	opts := to.New()
	opts.Name = "test"
	opts.Provider = "stdout"
	to.ProcessTracingOptions(to.Lookup{opts.Name: opts})

	return &tracing.Tracer{
		Name:    opts.Name,
		Tracer:  tp.Tracer(opts.Name),
		Options: opts,
	}, sr
}

// RequireSpanAttributes asserts that the most recently ended span named spanName has all wanted attributes.
func RequireSpanAttributes(t testing.TB, sr *tracetest.SpanRecorder, spanName string, want map[string]string) {
	t.Helper()

	attrs := spanAttributes(t, sr, spanName)
	for k, v := range want {
		got, ok := attrs[k]
		if !ok {
			t.Fatalf("span %q missing attribute %q", spanName, k)
		}
		gotString := fmt.Sprint(got.AsInterface())
		if gotString != v {
			t.Fatalf("span %q attribute %q: expected %q, got %q", spanName, k, v, gotString)
		}
	}
}

// RequireNoSpanAttribute asserts that the most recently ended span named spanName does not have key.
func RequireNoSpanAttribute(t testing.TB, sr *tracetest.SpanRecorder, spanName, key string) {
	t.Helper()

	attrs := spanAttributes(t, sr, spanName)
	if _, ok := attrs[key]; ok {
		t.Fatalf("span %q must not include attribute %q", spanName, key)
	}
}

func spanAttributes(t testing.TB, sr *tracetest.SpanRecorder, spanName string) map[string]attribute.Value {
	t.Helper()

	spans := sr.Ended()
	for _, v := range slices.Backward(spans) {
		if v.Name() != spanName {
			continue
		}
		out := make(map[string]attribute.Value, len(v.Attributes()))
		for _, kv := range v.Attributes() {
			out[string(kv.Key)] = kv.Value
		}
		return out
	}

	t.Fatalf("span %q not found in %d ended spans", spanName, len(spans))
	return nil
}
