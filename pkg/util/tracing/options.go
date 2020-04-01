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

const (
	SampleRateDefault float64 = 0
)

type ExporterOption func(*ExporterOptions)

type ExporterOptions struct {
	collectorURL string
	agentURL     string
	sampleRate   float64
	username     string
	password     string
}

func aggreagteOptions(opts []ExporterOption) *ExporterOptions {
	o := ExporterOptions{
		sampleRate: SampleRateDefault,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return &o
}

// WithCollector is an option that directs the exporter to export trace info to a collector URL
func WithCollector(uri string) ExporterOption {
	return func(e *ExporterOptions) {
		e.collectorURL = uri
	}
}

// Option that directs the collector to report to a local agent, rather than a remote collector
func WithAgent(uri string) ExporterOption {
	return func(e *ExporterOptions) {
		e.agentURL = uri
	}
}

// Option that sets the sample rate for the collector
func WithSampleRate(rate float64) ExporterOption {
	return func(e *ExporterOptions) {
		e.sampleRate = rate
	}
}

// WithUsername is an option that sets the username for remote server collector connections
func WithUsername(username string) ExporterOption {
	return func(e *ExporterOptions) {
		e.username = username
	}
}

// WithPassword is an option that sets the password for remote server collector connections
func WithPassword(password string) ExporterOption {
	return func(e *ExporterOptions) {
		e.password = password
	}
}

func NewExporterOptions(opts ...ExporterOption) *ExporterOptions {
	options := ExporterOptions{}
	for _, o := range opts {
		o(&options)
	}
	return &options
}
