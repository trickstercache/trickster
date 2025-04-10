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

// Package registration registers configured tracers for use with handlers
package registration

import (
	"errors"
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/exporters/noop"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/exporters/otlp"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/exporters/stdout"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/exporters/zipkin"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/providers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// RegisterAll registers all Tracers in the provided configuration, and returns
// their Flushers
func RegisterAll(cfg *config.Config, isDryRun bool) (tracing.Tracers, error) {
	if cfg == nil {
		return nil, errors.New("no config provided")
	}
	if cfg.Backends == nil {
		return nil, errors.New("no backends provided")
	}
	if cfg.TracingConfigs == nil {
		return nil, errors.New("no tracers provided")
	}

	// remove any tracers that are configured but not used by a backend, we don't want
	// to use resources to instantiate them
	mappedTracers := sets.NewStringSet()

	for k, v := range cfg.Backends {
		if v != nil && v.TracingConfigName != "" {
			if _, ok := cfg.TracingConfigs[v.TracingConfigName]; !ok {
				return nil, fmt.Errorf("backend %s provided invalid tracing config name %s",
					k, v.TracingConfigName)
			}
			mappedTracers.Add(v.TracingConfigName)
		}
	}

	tracers := make(tracing.Tracers)
	for k, tc := range cfg.TracingConfigs {
		if !mappedTracers.Contains(k) {
			// tracer is configured, but not mapped by any backend,
			// so we won't instantiate it
			continue
		}

		tc.Name = k
		if _, ok := providers.Names[tc.Provider]; !ok {
			return nil, fmt.Errorf("invalid tracer type [%s] for tracing config [%s]",
				tc.Provider, k)
		}
		tracer, err := GetTracer(tc, isDryRun)
		if err != nil {
			return nil, err
		}
		tracers[k] = tracer
	}
	return tracers, nil
}

// GetTracer returns a *Tracer based on the provided options
func GetTracer(opts *options.Options,
	isDryRun bool) (*tracing.Tracer, error) {

	if opts == nil {
		logger.Info("nil tracing config, using noop tracer", nil)
		return noop.New(opts)
	}

	logTracerRegistration := func() {
		if isDryRun {
			return
		}
		logger.Info("tracer registration",
			logging.Pairs{
				"name":        opts.Name,
				"provider":    opts.Provider,
				"serviceName": opts.ServiceName,
				"endpoint":    opts.Endpoint,
				"sampleRate":  opts.SampleRate,
				"tags":        strings.StringMap(opts.Tags).String(),
			},
		)
	}

	switch opts.Provider {
	case providers.Stdout.String():
		logTracerRegistration()
		return stdout.New(opts)
	case providers.OTLP.String():
		logTracerRegistration()
		return otlp.New(opts)
	case providers.Zipkin.String():
		logTracerRegistration()
		return zipkin.New(opts)
	}

	return nil, nil
}
