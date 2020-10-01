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

// Package stdout provides a Stdout Tracer
package stdout

import (
	d "github.com/tricksterproxy/trickster/pkg/config/defaults"
	"github.com/tricksterproxy/trickster/pkg/tracing"
	"github.com/tricksterproxy/trickster/pkg/tracing/options"

	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/exporters/stdout"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// NewTracer returns a new Stdout Tracer
func NewTracer(opts *options.Options) (*tracing.Tracer, error) {

	var exp *stdout.Exporter
	var err error

	o := []stdout.Option{}

	if opts == nil {
		opts = &options.Options{
			SampleRate:  1,
			ServiceName: d.DefaultTracerServiceName,
			Provider:    "stdout",
		}
	}

	if opts != nil && opts.StdOutOptions != nil {
		if opts.StdOutOptions.PrettyPrint {
			o = append(o, stdout.WithPrettyPrint())
		}
	}

	// Create Basic Stdout Exporter
	exp, err = stdout.NewExporter(o...)
	if err != nil {
		return nil, err
	}

	var sampler sdktrace.Sampler

	switch opts.SampleRate {
	case 0:
		sampler = sdktrace.NeverSample()
	case 1:
		sampler = sdktrace.AlwaysSample()
	default:
		sampler = sdktrace.ProbabilitySampler(opts.SampleRate)
	}

	serviceKey := label.String("service.name", opts.ServiceName)

	var tags []label.KeyValue
	if opts.Tags != nil && len(opts.Tags) > 0 {
		tags = make([]label.KeyValue, 1, len(opts.Tags)+1)
		tags[0] = serviceKey
		for k, v := range opts.Tags {
			tags = append(tags, label.String(k, v))
		}
	} else {
		tags = []label.KeyValue{serviceKey}
	}

	tp, err := sdktrace.NewProvider(sdktrace.WithSyncer(exp),
		sdktrace.WithConfig(sdktrace.Config{DefaultSampler: sampler}),
		sdktrace.WithResource(resource.New(tags...)),
	)
	if err != nil {
		return nil, err
	}

	tracer := tp.Tracer(opts.Name)

	return &tracing.Tracer{
		Name:    opts.Name,
		Tracer:  tracer,
		Options: opts,
	}, nil

}
