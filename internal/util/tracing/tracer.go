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

package tracing

import (
	"go.opentelemetry.io/otel/api/trace"
)

// SetTracer sets up the requested tracer implementation
func SetTracer(impl TracerImplementation, exporter TraceExporter, opts ...ExporterOption) (trace.Tracer, func(), *recorderExporter, error) {
	var (
		tracer trace.Tracer
		flush  func()
		r      *recorderExporter
		err    error
	)

	options := aggreagteOptions(opts)

	switch exporter {
	case StdoutExporter:
		tracer, flush, r, err = setStdOutTracer(options)
	case JaegerExporter:
		tracer, flush, r, err = setJaegerExporter(options)
	case RecorderExporter:
		tracer, flush, r, err = setRecorderExporter(nil, options)
	default:
		tracer, flush, r, err = setNoopExporter()
	}

	switch impl {
	case OpenTracingTracer:
		fallthrough
	case OpenTelemetryTracer:
		fallthrough
	default:
		// do nothing
	}
	return tracer, flush, r, err
}
