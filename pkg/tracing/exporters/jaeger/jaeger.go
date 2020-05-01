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

// Package jaeger provides a Jager Tracer
package jaeger

import (
	"github.com/tricksterproxy/trickster/pkg/tracing"
	te "github.com/tricksterproxy/trickster/pkg/tracing/errors"
	"github.com/tricksterproxy/trickster/pkg/tracing/options"

	"go.opentelemetry.io/otel/api/core"
	"go.opentelemetry.io/otel/api/key"
	"go.opentelemetry.io/otel/exporters/trace/jaeger"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func NewTracer(options *options.Options) (*tracing.Tracer, error) {

	var tp *sdktrace.Provider
	var err error
	var flusher func()

	if options == nil {
		return nil, te.NoTracerOptions
	}

	var sampler sdktrace.Sampler
	switch options.SampleRate {
	case 0:
		sampler = sdktrace.NeverSample()
	case 1:
		sampler = sdktrace.AlwaysSample()
	default:
		sampler = sdktrace.ProbabilitySampler(options.SampleRate)
	}

	var tags []core.KeyValue
	if options.Tags != nil && len(options.Tags) > 0 {
		tags = make([]core.KeyValue, len(options.Tags))
		for k, v := range options.Tags {
			tags = append(tags, key.String(k, v))
		}
	}

	var eo jaeger.EndpointOption
	if options.JaegerOptions != nil {
		if options.JaegerOptions.EndpointType == "agent" {
			eo = jaeger.WithAgentEndpoint(options.CollectorURL)
		}
	}
	if eo == nil {
		ceo := make([]jaeger.CollectorEndpointOption, 0)
		if options.CollectorUser != "" {
			ceo = append(ceo, jaeger.WithUsername(options.CollectorUser))
		}
		if options.CollectorPass != "" {
			ceo = append(ceo, jaeger.WithPassword(options.CollectorPass))
		}
		eo = jaeger.WithCollectorEndpoint(options.CollectorURL, ceo...)
	}

	// Create Tracing Provider
	tp, flusher, err = jaeger.NewExportPipeline(eo,
		jaeger.WithSDK(&sdktrace.Config{DefaultSampler: sampler}),
		jaeger.WithProcess(jaeger.Process{
			ServiceName: options.ServiceName,
			Tags:        tags,
		}),
	)
	if err != nil {
		return nil, err
	}

	tracer := tp.Tracer(options.Name)

	return &tracing.Tracer{
		Name:    options.Name,
		Tracer:  tracer,
		Options: options,
		Flusher: flusher,
	}, nil

}
