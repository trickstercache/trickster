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
	"github.com/tricksterproxy/trickster/pkg/config/defaults"
	jaegeropts "github.com/tricksterproxy/trickster/pkg/tracing/exporters/jaeger/options"
	stdoutopts "github.com/tricksterproxy/trickster/pkg/tracing/exporters/stdout/options"
	"github.com/tricksterproxy/trickster/pkg/util/strings"
)

// Options is a Tracing Options collection
type Options struct {
	Name          string            `toml:"-"`
	TracerType    string            `toml:"tracer_type"`
	ServiceName   string            `toml:"service_name"`
	CollectorURL  string            `toml:"collector_url"`
	CollectorUser string            `toml:"collector_user"`
	CollectorPass string            `toml:"collector_pass"`
	SampleRate    float64           `toml:"sample_rate"`
	Tags          map[string]string `toml:"tags"`

	StdOutOptions *stdoutopts.Options `toml:"stdout"`
	JaegerOptions *jaegeropts.Options `toml:"jaeger"`
}

// NewOptions returns a new *Options with the default values
func NewOptions() *Options {
	return &Options{
		TracerType:    defaults.DefaultTracerType,
		ServiceName:   defaults.DefaultTracerServiceName,
		StdOutOptions: &stdoutopts.Options{},
		JaegerOptions: &jaegeropts.Options{},
	}
}

// Clone returns an exact copy of a tracing config
func (o *Options) Clone() *Options {
	var so *stdoutopts.Options
	if o.StdOutOptions != nil {
		so = o.StdOutOptions.Clone()
	}
	var jo *jaegeropts.Options
	if o.JaegerOptions != nil {
		jo = o.JaegerOptions.Clone()
	}
	return &Options{
		Name:          o.Name,
		TracerType:    o.TracerType,
		ServiceName:   o.ServiceName,
		CollectorURL:  o.CollectorURL,
		CollectorUser: o.CollectorUser,
		CollectorPass: o.CollectorPass,
		SampleRate:    o.SampleRate,
		Tags:          strings.CloneMap(o.Tags),
		StdOutOptions: so,
		JaegerOptions: jo,
	}
}

// ProcessTracingOptions enriches the configuration data of the provided Tracing Options collection
func ProcessTracingOptions(mo map[string]*Options, metadata *toml.MetaData) {
	if metadata == nil || mo == nil {
		return
	}
	for k, v := range mo {
		if !metadata.IsDefined("tracing", k, "sample_rate") {
			v.SampleRate = 1
		}
		if !metadata.IsDefined("tracing", k, "service_name") {
			v.ServiceName = defaults.DefaultTracerServiceName
		}
		if !metadata.IsDefined("tracing", k, "tracer_type") {
			v.TracerType = defaults.DefaultTracerType
		}
	}
}
