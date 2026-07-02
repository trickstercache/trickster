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

package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestHTTPToCode(t *testing.T) {
	tests := []struct {
		code     int
		expected codes.Code
	}{
		{
			http.StatusMovedPermanently, codes.Ok,
		},
		{
			http.StatusNotFound, codes.Error,
		},
		{
			http.StatusBadRequest, codes.Error,
		},
		{
			http.StatusServiceUnavailable, codes.Error,
		},
		{
			http.StatusInternalServerError, codes.Error,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			v := HTTPToCode(test.code)
			if v != test.expected {
				t.Errorf("expected %d got %d", test.expected, v)
			}
		})
	}
}

func TestTags(t *testing.T) {
	t1 := Tags{"testKey1": "testValue1"}
	t2 := Tags{"testKey2": "testValue2"}

	t1.Merge(nil)
	if len(t1) != 1 {
		t.Errorf("expected %d got %d", 1, len(t1))
	}

	t1.Merge(t2)
	if len(t1) != 2 {
		t.Errorf("expected %d got %d", 2, len(t1))
	}

	t1.MergeAttr(nil)
	if len(t1) != 2 {
		t.Errorf("expected %d got %d", 2, len(t1))
	}

	attrs := []attribute.KeyValue{attribute.String("testKey3", "testValue3")}
	t1.MergeAttr(attrs)
	if len(t1) != 3 {
		t.Errorf("expected %d got %d", 3, len(t1))
	}

	attrs = t1.ToAttr()
	if len(attrs) != 3 {
		t.Errorf("expected %d got %d", 3, len(attrs))
	}
}

func TestConfigurePropagatorsInstallsTraceContextAndBaggage(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{17, 18, 19, 20, 21, 22, 23, 24}
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	bg, err := baggage.Parse("tenant=alpha")
	if err != nil {
		t.Fatal(err)
	}
	ctx := baggage.ContextWithBaggage(
		trace.ContextWithSpanContext(context.Background(), spanCtx),
		bg,
	)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	if got := req.Header.Get("traceparent"); got != "" {
		t.Fatalf("expected empty traceparent before ConfigurePropagators, got %q", got)
	}

	ConfigurePropagators()

	req = httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	wantTraceparent := "00-" + traceID.String() + "-" + spanID.String() + "-01"
	if got := req.Header.Get("traceparent"); got != wantTraceparent {
		t.Fatalf("traceparent: expected %q, got %q", wantTraceparent, got)
	}
	if got := req.Header.Get("baggage"); got != "tenant=alpha" {
		t.Fatalf("baggage: expected %q, got %q", "tenant=alpha", got)
	}

	extracted := otel.GetTextMapPropagator().Extract(
		context.Background(),
		propagation.HeaderCarrier(req.Header),
	)
	gotSpanCtx := trace.SpanContextFromContext(extracted)
	if !gotSpanCtx.IsRemote() {
		t.Fatal("expected extracted span context to be remote")
	}
	if gotSpanCtx.TraceID() != traceID || gotSpanCtx.SpanID() != spanID {
		t.Fatalf("extracted span context = %s/%s, want %s/%s",
			gotSpanCtx.TraceID(), gotSpanCtx.SpanID(), traceID, spanID)
	}
	if got := baggage.FromContext(extracted).Member("tenant").Value(); got != "alpha" {
		t.Fatalf("extracted baggage tenant: expected %q, got %q", "alpha", got)
	}
}

func TestSamplerHonorsRemoteParentSampling(t *testing.T) {
	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{17, 18, 19, 20, 21, 22, 23, 24}
	sampleRate := 0.0
	sampler := Sampler(&options.Options{SampleRate: &sampleRate})

	root := sampler.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: context.Background(),
		TraceID:       traceID,
		Name:          "root",
	})
	if root.Decision != sdktrace.Drop {
		t.Fatalf("root decision = %v, want Drop", root.Decision)
	}

	sampledParent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	sampledChild := sampler.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: trace.ContextWithRemoteSpanContext(context.Background(), sampledParent),
		TraceID:       traceID,
		Name:          "sampled-child",
	})
	if sampledChild.Decision != sdktrace.RecordAndSample {
		t.Fatalf("sampled remote parent decision = %v, want RecordAndSample", sampledChild.Decision)
	}

	unsampledParent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
		Remote:  true,
	})
	unsampledChild := sampler.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: trace.ContextWithRemoteSpanContext(context.Background(), unsampledParent),
		TraceID:       traceID,
		Name:          "unsampled-child",
	})
	if unsampledChild.Decision != sdktrace.Drop {
		t.Fatalf("unsampled remote parent decision = %v, want Drop", unsampledChild.Decision)
	}
}
