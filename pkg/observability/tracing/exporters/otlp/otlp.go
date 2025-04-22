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

// Package otlp provides a OTLP Tracer
package otlp

import (
	"context"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	errs "github.com/trickstercache/trickster/v2/pkg/observability/tracing/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"

	"go.opentelemetry.io/otel/attribute"
	otlp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// New returns a new OTLP Tracer based on the provided options
func New(o *options.Options) (*tracing.Tracer, error) {
	var tp trace.TracerProvider
	var err error

	if o == nil {
		return nil, errs.ErrNoTracerOptions
	}

	var sampler sdktrace.Sampler
	switch o.SampleRate {
	case 0:
		sampler = sdktrace.NeverSample()
	case 1:
		sampler = sdktrace.AlwaysSample()
	default:
		sampler = sdktrace.TraceIDRatioBased(o.SampleRate)
	}

	var tags []attribute.KeyValue
	if len(o.Tags) > 0 {
		tags = make([]attribute.KeyValue, len(o.Tags))
		for k, v := range o.Tags {
			tags = append(tags, attribute.String(k, v))
		}
	}

	opts := make([]otlp.Option, 0, 10)
	// this determines if the collector endpoint is a path, uri or url
	// and calls the appropriate Options decorator
	switch {
	case strings.HasPrefix(o.Endpoint, "/"):
		opts = append(opts, otlp.WithURLPath(o.Endpoint))
	case strings.HasPrefix(o.Endpoint, "http"):
		opts = append(opts, otlp.WithEndpointURL(o.Endpoint))
		if !strings.HasPrefix(o.Endpoint, "https") {
			opts = append(opts, otlp.WithInsecure())
		}
	default:
		otlp.WithEndpoint(o.Endpoint)
	}

	if o.Timeout > 0 {
		opts = append(opts, otlp.WithTimeout(o.Timeout))
	}

	if len(o.Headers) > 0 {
		opts = append(opts, otlp.WithHeaders(o.Headers))
	}

	if !o.DisableCompression {
		opts = append(opts, otlp.WithCompression(otlp.GzipCompression))
	}

	exporter, err := otlp.New(context.Background(), opts...)
	if err != nil {
		return nil, err
	}

	tracerOpts := make([]sdktrace.TracerProviderOption, 0, 3)
	tracerOpts = append(tracerOpts, sdktrace.WithSampler(sampler))
	if len(tags) > 0 {
		tracerOpts = append(tracerOpts,
			sdktrace.WithResource(resource.NewWithAttributes("", tags...)))
	}
	tracerOpts = append(tracerOpts, sdktrace.WithBatcher(exporter))
	tp = sdktrace.NewTracerProvider(tracerOpts...)
	tracer := tp.Tracer(o.Name)

	return &tracing.Tracer{
		Name:         o.Name,
		Tracer:       tracer,
		Options:      o,
		ShutdownFunc: exporter.Shutdown,
	}, nil
}
