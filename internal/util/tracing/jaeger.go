/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package tracing

import (
	"go.opentelemetry.io/otel/api/core"
	"go.opentelemetry.io/otel/api/key"
	"go.opentelemetry.io/otel/api/trace"
	"go.opentelemetry.io/otel/exporter/trace/jaeger"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func setJaegerExporter(collectorURL string, sampleRate float64) (trace.Tracer, func(), *recorderExporter, error) {
	exporter, err := jaeger.NewExporter(
		jaeger.WithCollectorEndpoint(collectorURL),
		jaeger.WithProcess(jaeger.Process{
			ServiceName: serviceName,
			Tags: []core.KeyValue{
				key.String("exporter", "jaeger"),
			},
		}),
	)
	if err != nil {
		return nil, func() {}, nil, err
	}

	tp, err := sdktrace.NewProvider(
		sdktrace.WithConfig(sdktrace.Config{DefaultSampler: sdktrace.ProbabilitySampler(sampleRate)}),
		sdktrace.WithSyncer(exporter))
	if err != nil {
		return tp.Tracer(""), func() {}, nil, err
	}

	return tp.Tracer(""), func() {
		exporter.Flush()
	}, nil, nil
}
