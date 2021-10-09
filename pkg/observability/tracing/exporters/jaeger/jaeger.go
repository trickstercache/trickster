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

// Package jaeger provides a Jaeger Tracer
package jaeger

import (
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	errs "github.com/trickstercache/trickster/v2/pkg/observability/tracing/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/jaeger"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// New returns a new Jaeger Tracer based on the provided options
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

	var eo jaeger.EndpointOption
	if options.JaegerOptions != nil {
		if options.JaegerOptions.EndpointType == "agent" {
			parts := strings.Split(options.CollectorURL, ":")
			if len(parts) != 2 {
				return nil, errs.ErrInvalidEndpointURL
			}
			eo = jaeger.WithAgentEndpoint(jaeger.WithAgentHost(parts[0]), jaeger.WithAgentPort(parts[1]))
		}
	}
	if eo == nil { // by default assume a "collector" config
		ceo := make([]jaeger.CollectorEndpointOption, 1, 3)
		ceo[0] = jaeger.WithEndpoint(options.CollectorURL)
		if options.CollectorUser != "" {
			ceo = append(ceo, jaeger.WithUsername(options.CollectorUser))
		}
		if options.CollectorPass != "" {
			ceo = append(ceo, jaeger.WithPassword(options.CollectorPass))
		}
		eo = jaeger.WithCollectorEndpoint(ceo...)
	}

	exporter, err := jaeger.New(eo)
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
