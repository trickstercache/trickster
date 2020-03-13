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

package options

import (
	"github.com/BurntSushi/toml"
	d "github.com/Comcast/trickster/internal/config/defaults"

	"go.opentelemetry.io/otel/api/trace"
)

type TracingExporterOptions struct {
	Exporter string `toml:"type"`
	// Collectoris the URL of the trace collector it MUST be of Implementation implementation
	Collector string `toml:"collector"`
	// Agent is the URL of the trace agent.
	Agent string `toml:"agent"`
	// SampleRate sets the probability that a span will be recorded. Values between 0 and 1 are accepted.
	SampleRate float64 `toml:"sample_rate"`
}

// Options provides the distributed tracing configuration
type Options struct {
	// Name is the name of the Tracing Config
	Name string `toml:"-"`
	// Implementation is the particular implementation to use. Ex OpenTelemetry.
	// TODO generate with Rob Pike's Stringer
	Implementation string `toml:"implementation"`
	// Exporter is the format used to send to the collector
	Exporter *TracingExporterOptions `toml:"exporter"`

	// Tracer is the actual Tracer Object associated with this configuration once loaded
	Tracer trace.Tracer `toml:"-"`
}

// NewOptions returns a new tracing config with default values
func NewOptions() *Options {
	return &Options{
		Name:           "default",
		Implementation: d.DefaultTracerImplemetation,
		Exporter: &TracingExporterOptions{
			Exporter:   d.DefaultExporterImplementation,
			SampleRate: 1,
		},
	}
}

// Clone returns an exact copy of a tracing config
func (tc *Options) Clone() *Options {
	return &Options{
		Name:           tc.Name,
		Implementation: tc.Implementation,
		Exporter:       tc.Exporter.Clone(),
		Tracer:         tc.Tracer,
	}
}

// Clone returns an exact copy of exporter options
func (teo *TracingExporterOptions) Clone() *TracingExporterOptions {
	return &TracingExporterOptions{
		Exporter:   teo.Exporter,
		SampleRate: teo.SampleRate,
		Collector:  teo.Collector,
	}

}

func ProcessTracingConfigs(mo map[string]*Options, metadata *toml.MetaData) {
	for k, v := range mo {
		if !metadata.IsDefined("tracing", k, "exporter") {
			v.Exporter = &TracingExporterOptions{}
		}
		// if the user does not provide a sample rate in the config, assume they want 100% sampling
		if !metadata.IsDefined("tracing", k, "exporter", "sample_rate") {
			v.Exporter.SampleRate = 1
		}
	}
}
