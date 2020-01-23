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
	"github.com/Comcast/trickster/internal/util/log"
	"go.opentelemetry.io/otel/api/trace"
)

// TracerImplementation members
type TracerImplementation int

const (
	// NoopTracer indicates a Tracer Implementation wherein all methods are no-ops.
	// This should be used when tracing is not enabled or not sampled.
	NoopTracer TracerImplementation = iota
	// RecorderTracer represents the Recorder Tracer Implementation
	RecorderTracer
	// StdoutTracer represents the Standard Output Tracer Implementation
	StdoutTracer
	// JaegerTracer represents the Jaeger Tracing Tracer Implementation
	JaegerTracer
)

var (
	tracerImplementationStrings = []string{
		"noop",
		"recorder",
		"stdout",
		"jaeger",
	}

	// TracerImplementations is map of TracerImplementations accessible by their string value
	TracerImplementations = map[string]TracerImplementation{
		tracerImplementationStrings[NoopTracer]:     NoopTracer,
		tracerImplementationStrings[RecorderTracer]: RecorderTracer,
		tracerImplementationStrings[StdoutTracer]:   StdoutTracer,
		// TODO New Implementations go here
		tracerImplementationStrings[JaegerTracer]: JaegerTracer,
	}
)

func (t TracerImplementation) String() string {
	if t < NoopTracer || t > JaegerTracer {
		return "unknown-tracer"
	}
	return tracerImplementationStrings[t]
}

// SetTracer sets up the requested tracer implementation
func SetTracer(t TracerImplementation, collectorURL string, sampleRate float64) (trace.Tracer, func(), error) {
	switch t {
	case StdoutTracer:
		return setStdOutTracer(sampleRate)
	case JaegerTracer:
		return setJaegerTracer(collectorURL, sampleRate)
	case RecorderTracer:
		// TODO make recorder available at runtime
		tracer, flush, _, err := setRecorderTracer(
			// Only called if there is an error so the log message won't be evaluated otherwise
			func(err error) {
				pairs := log.Pairs{
					"Error":                err,
					"TracerImplementation": tracerImplementationStrings[t],
					"Collector":            collectorURL,
					"SampleRate":           sampleRate,
				}
				log.Error(
					"Trace Recorder Error",
					pairs,
				)
			},
			sampleRate,
		)
		return tracer, flush, err
	default:
		return setNoopTracer()
	}
}
