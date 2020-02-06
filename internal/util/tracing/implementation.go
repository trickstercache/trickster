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

// TracerImplementation defines the implementation of Tracer to Use
type TracerImplementation int

const (
	// OpenTelemetryTracer is a tracer that accepts the Open Telemetry standard tracing format.
	OpenTelemetryTracer TracerImplementation = iota
	// OpenTracingTracer accepts and uses traces in the OpenTracing format
	OpenTracingTracer
)

var (
	// TracerImplementations is map of TracerImplementations accessible by their string value
	TracerImplementations = map[string]TracerImplementation{
		"opentelemetry": OpenTelemetryTracer,
		"opentracing":   OpenTracingTracer,
	}
	// TracerImplementationStrings is the reverse map of TracerImplementations
	TracerImplementationStrings = map[TracerImplementation]string{}
)

func init() {
	// create inverse lookup map
	for k, v := range TracerImplementations {
		TracerImplementationStrings[v] = k
	}
}

func (t TracerImplementation) String() string {
	if v, ok := TracerImplementationStrings[t]; ok {
		return v
	}
	return "unknown-tracer"
}
