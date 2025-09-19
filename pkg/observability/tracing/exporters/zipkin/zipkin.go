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

// Package zipkin provides a Zipkin Tracer
package zipkin

import (
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	errs "github.com/trickstercache/trickster/v2/pkg/observability/tracing/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"

	"go.opentelemetry.io/otel/exporters/zipkin"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// New returns a new Zipkin Tracer
func New(opts *options.Options) (*tracing.Tracer, error) {
	if opts == nil {
		return nil, errs.ErrNoTracerOptions
	}
	var tp *sdktrace.TracerProvider
	var err error
	var sampler sdktrace.Sampler
	opts.SanitizeSampleRate()
	switch *opts.SampleRate {
	case 0:
		sampler = sdktrace.NeverSample()
	case 1:
		sampler = sdktrace.AlwaysSample()
	default:
		sampler = sdktrace.TraceIDRatioBased(*opts.SampleRate)
	}
	exporter, err := zipkin.New(
		opts.Endpoint,
	)
	if err != nil {
		return nil, err
	}

	tp = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(5),
			sdktrace.WithMaxExportBatchSize(10),
		),
		sdktrace.WithSampler(sampler),
	)

	tracer := tp.Tracer(opts.Name)

	return &tracing.Tracer{
		Name:    opts.Name,
		Tracer:  tracer,
		Options: opts,
	}, nil

}
