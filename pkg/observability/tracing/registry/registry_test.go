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

package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestRegisterAll(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	// test nil config
	f, err := RegisterAll(nil, true)
	if err == nil {
		t.Error("expected error for no config provided")
	}
	if len(f) > 0 {
		t.Errorf("expected %d got %d", 0, len(f))
	}

	// test good config
	f, err = RegisterAll(config.NewConfig(), true)
	if err != nil {
		t.Error(err)
	}
	if len(f) != 1 {
		t.Errorf("expected %d got %d", 1, len(f))
	}

	// test bad implementation
	cfg := config.NewConfig()
	tc := options.New()

	cfg.TracingOptions = make(options.Lookup)
	cfg.TracingOptions["test"] = tc
	cfg.TracingOptions["test3"] = tc
	cfg.Backends["default"].TracingConfigName = "test"

	_, err = RegisterAll(cfg, true)
	if err != nil {
		t.Error(err)
	}

	tc.Provider = "otlp"
	tc.Endpoint = "http://example.com"
	_, err = RegisterAll(cfg, false)
	if err != nil {
		t.Error(err)
	}

	tc.Provider = "stdout"
	_, err = RegisterAll(cfg, true)
	if err != nil {
		t.Error(err)
	}

	tc.Provider = "foo"

	_, err = RegisterAll(cfg, true)
	if err == nil {
		t.Error("expected error for invalid provider")
	}

	// test empty implementation
	tc.Provider = ""
	f, _ = RegisterAll(cfg, true)
	if len(f) > 0 {
		t.Errorf("expected %d got %d", 0, len(f))
	}

	tc.Provider = "none"
	cfg.Backends["default"].TracingConfigName = "test2"
	_, err = RegisterAll(cfg, true)
	if err == nil {
		t.Error("expected error for invalid tracing config name")
	}
	cfg.Backends["default"].TracingConfigName = "test"

	temp := cfg.TracingOptions
	cfg.TracingOptions = nil
	// test nil tracing config
	f, _ = RegisterAll(cfg, true)
	if len(f) > 0 {
		t.Errorf("expected %d got %d", 0, len(f))
	}
	cfg.TracingOptions = temp

	// test nil backend options
	cfg.Backends = nil
	_, err = RegisterAll(cfg, true)
	if err == nil {
		t.Error("expected error for invalid tracing implementation")
	}
}

func TestGetTracer(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	tr, _ := GetTracer(nil, true)
	if tr != nil {
		t.Error("expected nil tracer")
	}
}

func TestGetTracerInstallsTraceContextPropagator(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

	opts := options.New()
	opts.Name = "test"
	opts.Provider = "stdout"
	tracer, err := GetTracer(opts, true)
	if err != nil {
		t.Fatal(err)
	}
	if tracer == nil {
		t.Fatal("expected tracer")
	}

	traceID := trace.TraceID{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	spanID := trace.SpanID{2, 2, 2, 2, 2, 2, 2, 2}
	ctx := trace.ContextWithSpanContext(context.Background(),
		trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    traceID,
			SpanID:     spanID,
			TraceFlags: trace.FlagsSampled,
		}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	want := "00-" + traceID.String() + "-" + spanID.String() + "-01"
	if got := req.Header.Get("traceparent"); got != want {
		t.Fatalf("traceparent: expected %q, got %q", want, got)
	}
}
