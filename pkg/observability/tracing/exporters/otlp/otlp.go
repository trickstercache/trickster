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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	errs "github.com/trickstercache/trickster/v2/pkg/observability/tracing/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"

	"go.opentelemetry.io/otel/attribute"
	otlp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// New returns a new OTLP Tracer based on the provided options
func New(options *options.Options) (*tracing.Tracer, error) {

	var tp trace.TracerProvider
	var err error

	if options == nil {
		return nil, errs.ErrNoTracerOptions
	}

	var sampler sdktrace.Sampler
	switch options.SampleRate {
	case 0:
		sampler = sdktrace.NeverSample()
	case 1:
		sampler = sdktrace.AlwaysSample()
	default:
		sampler = sdktrace.TraceIDRatioBased(options.SampleRate)
	}

	var tags []attribute.KeyValue
	if options.Tags != nil && len(options.Tags) > 0 {
		tags = make([]attribute.KeyValue, len(options.Tags))
		for k, v := range options.Tags {
			// TODO: just discovered that these aren't actually being used
			tags = append(tags, attribute.String(k, v))
		}
	}

	opts := make([]otlp.Option, 0, 10)
	// this determines if the collector endpoint is a path, uri or url
	// and calls the appropriate Options decorator
	if strings.HasPrefix(options.Endpoint, "/") {
		opts = append(opts, otlp.WithURLPath(options.Endpoint))
	} else if strings.HasPrefix(options.Endpoint, "http") {
		opts = append(opts, otlp.WithEndpointURL(options.Endpoint))
		if !strings.HasPrefix(options.Endpoint, "https") {
			opts = append(opts, otlp.WithInsecure())
		}
	} else {
		otlp.WithEndpoint(options.Endpoint)
	}

	if options.TimeoutMS > 0 {
		opts = append(opts, otlp.WithTimeout(
			time.Millisecond*time.Duration(int64(options.TimeoutMS))),
		)
	}

	if len(options.Headers) > 0 {
		opts = append(opts, otlp.WithHeaders(options.Headers))
	}

	if !options.DisableCompression {
		opts = append(opts, otlp.WithCompression(otlp.GzipCompression))
	}

	exporter, err := otlp.New(context.Background(), opts...)
	if err != nil {
		return nil, err
	}

	tp = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sampler),
	)

	tracer := tp.Tracer(options.Name)

	return &tracing.Tracer{
		Name:         options.Name,
		Tracer:       tracer,
		Options:      options,
		ShutdownFunc: exporter.Shutdown,
	}, nil

}
