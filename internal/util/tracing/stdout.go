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
	"go.opentelemetry.io/otel/api/trace"
	"go.opentelemetry.io/otel/exporter/trace/stdout"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SetStdOutTracer set a std out only tracer
// It serves as a fallback and was created referencing
// https://github.com/open-telemetry/opentelemetry-go#quick-start
func setStdOutTracer(sampleRate float64) (trace.Tracer, func(), *recorderExporter, error) {
	f := func() {}
	// Create stdout exporter to be able to retrieve
	// the collected spans.
	exporter, _ := stdout.NewExporter(stdout.Options{PrettyPrint: true})

	tp, err := sdktrace.NewProvider(sdktrace.WithConfig(sdktrace.Config{DefaultSampler: sdktrace.ProbabilitySampler(sampleRate)}),
		sdktrace.WithSyncer(exporter))

	return tp.Tracer(""), f, nil, err
}
