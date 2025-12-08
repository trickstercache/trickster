/*
 * Copyright 2018 The Trickster Authors
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
	"maps"
	"slices"
	"time"

	stdoutopts "github.com/trickstercache/trickster/v2/pkg/observability/tracing/exporters/stdout/options"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// Options is a Tracing Options collection
type Options struct {
	Name               string            `yaml:"-"`
	Provider           string            `yaml:"provider,omitempty"`
	ServiceName        string            `yaml:"service_name,omitempty"`
	Endpoint           string            `yaml:"endpoint,omitempty"`
	Timeout            time.Duration     `yaml:"timeout,omitempty"`
	Headers            map[string]string `yaml:"headers,omitempty"`
	DisableCompression bool              `yaml:"disable_compression,omitempty"`
	SampleRate         *float64          `yaml:"sample_rate,omitempty"`
	Tags               map[string]string `yaml:"tags,omitempty"`
	OmitTagsList       []string          `yaml:"omit_tags,omitempty"`

	StdOutOptions *stdoutopts.Options `yaml:"stdout,omitempty"`

	OmitTags sets.Set[string] `yaml:"-"`
	// for tracers that don't support WithProcess (e.g., Zipkin)
	attachTagsToSpan bool
}

// Lookup is a map of Options keyed by Options Name
type Lookup map[string]*Options

// New returns a new *Options with the default values
func New() *Options {
	return &Options{
		Provider:      DefaultTracerProvider,
		ServiceName:   DefaultTracerServiceName,
		StdOutOptions: &stdoutopts.Options{},
	}
}

// Clone returns an exact copy of a tracing config
func (o *Options) Clone() *Options {
	var so *stdoutopts.Options
	if o.StdOutOptions != nil {
		so = o.StdOutOptions.Clone()
	}
	out := pointers.Clone(o)
	out.StdOutOptions = so
	out.Tags = maps.Clone(o.Tags)
	out.OmitTagsList = slices.Clone(o.OmitTagsList)
	if o.SampleRate != nil {
		out.SampleRate = pointers.New(*o.SampleRate)
	}
	return out
}

// ProcessTracingOptions enriches the configuration data of the provided Tracing Options collection
func ProcessTracingOptions(mo Lookup) {
	if len(mo) == 0 {
		return
	}
	for _, v := range mo {
		v.SanitizeSampleRate()
		if v.ServiceName == "" {
			v.ServiceName = DefaultTracerServiceName
		}
		if v.Provider == "" {
			v.Provider = DefaultTracerProvider
		}
		v.generateOmitTags()
		v.setAttachTags()
	}
}

func (o *Options) SanitizeSampleRate() {
	switch {
	case o.SampleRate == nil || *o.SampleRate > 1:
		o.SampleRate = pointers.New(1.0)
	case *o.SampleRate < 0:
		o.SampleRate = pointers.New(0.0)
	}
}

func (o *Options) generateOmitTags() {
	o.OmitTags = sets.New(o.OmitTagsList)
}

// AttachTagsToSpan indicates that Tags should be attached to the span
func (o *Options) AttachTagsToSpan() bool {
	return o.attachTagsToSpan
}

func (o *Options) setAttachTags() {
	if o.Provider == "zipkin" && o.Tags != nil && len(o.Tags) > 0 {
		o.attachTagsToSpan = true
	}
}

func (o *Options) Valdiate() error {
	// placeholder for future validations (currently there are none for tracing)
	return nil
}

func (l Lookup) Validate() error {
	for k, o := range l {
		o.Name = k
		if err := o.Valdiate(); err != nil {
			return err
		}
	}
	return nil
}

func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type loadOptions Options
	lo := loadOptions(*(New()))
	if err := unmarshal(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
