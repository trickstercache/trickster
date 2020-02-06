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

// TraceExporter defines the implementation of Tracer Exporter to Use
type TraceExporter int

const (
	// NoopExporter indicates a Exporter Implementation wherein all methods are no-ops.
	// This should be used when tracing is not enabled or not sampled.
	NoopExporter TraceExporter = iota
	// RecorderExporter represents the Recorder Export Implementation
	RecorderExporter
	// StdoutExporter represents the Standard Output Exporter Implementation
	StdoutExporter
	// JaegerExporter represents the Jaeger Tracing Exporter Implementation
	JaegerExporter
)

var (
	// TraceExporters is map of TraceExporters accessible by their string value
	TraceExporters = map[string]TraceExporter{
		"noop":     NoopExporter,
		"recorder": RecorderExporter,
		"stdout":   StdoutExporter,
		"jaeger":   JaegerExporter,
	}
	// TraceExporterStrings is the reverse map of TraceExporters
	TraceExporterStrings = map[TraceExporter]string{}
)

func init() {
	// create inverse lookup map
	for k, v := range TraceExporters {
		TraceExporterStrings[v] = k
	}
}

func (t TraceExporter) String() string {
	if v, ok := TraceExporterStrings[t]; ok {
		return v
	}
	return "unknown-exporter"
}
