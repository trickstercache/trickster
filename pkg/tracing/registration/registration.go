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

// Package registration registers configured tracers for use with handlers
package registration

import (
	"errors"
	"fmt"

	"github.com/tricksterproxy/trickster/pkg/config"
	"github.com/tricksterproxy/trickster/pkg/tracing"
	"github.com/tricksterproxy/trickster/pkg/tracing/exporters/jaeger"
	"github.com/tricksterproxy/trickster/pkg/tracing/exporters/noop"
	"github.com/tricksterproxy/trickster/pkg/tracing/exporters/stdout"
	"github.com/tricksterproxy/trickster/pkg/tracing/exporters/zipkin"
	"github.com/tricksterproxy/trickster/pkg/tracing/options"
	"github.com/tricksterproxy/trickster/pkg/tracing/types"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
	"github.com/tricksterproxy/trickster/pkg/util/strings"
)

// RegisterAll registers all Tracers in the provided configuration, and returns
// their Flushers
func RegisterAll(cfg *config.Config, log *tl.Logger, isDryRun bool) (tracing.Tracers, error) {
	if cfg == nil {
		return nil, errors.New("no config provided")
	}
	if cfg.Origins == nil {
		return nil, errors.New("no origins provided")
	}
	if cfg.TracingConfigs == nil {
		return nil, errors.New("no tracers provided")
	}

	// remove any tracers that are configured but not used by an origin, we don't want
	// to use resources to instantiate them
	mappedTracers := make(map[string]bool)

	for k, v := range cfg.Origins {
		if v != nil && v.TracingConfigName != "" {
			if _, ok := cfg.TracingConfigs[v.TracingConfigName]; !ok {
				return nil, fmt.Errorf("origin %s provided invalid tracing config name %s",
					k, v.TracingConfigName)
			}
			mappedTracers[v.TracingConfigName] = true
		}
	}

	tracers := make(tracing.Tracers)
	for k, tc := range cfg.TracingConfigs {
		if _, ok := mappedTracers[k]; !ok {
			// tracer is configured, but not mapped by any origin,
			// so we won't instantiate it
			continue
		}

		tc.Name = k
		if _, ok := types.Names[tc.TracerType]; !ok {
			return nil, fmt.Errorf("invalid tracer type [%s] for tracing config [%s]",
				tc.TracerType, k)
		}
		tracer, err := GetTracer(tc, log, isDryRun)
		if err != nil {
			return nil, err
		}
		tracers[k] = tracer
	}
	return tracers, nil
}

// GetTracer returns a *Tracer based on the provided options
func GetTracer(options *options.Options, log *tl.Logger, isDryRun bool) (*tracing.Tracer, error) {

	if options == nil {
		log.Info("nil tracing config, using noop tracer", nil)
		return noop.NewTracer(options)
	}

	logTracerRegistration := func() {
		if isDryRun {
			return
		}
		log.Info(
			"tracer registration",
			tl.Pairs{
				"name":         options.Name,
				"tracerType":   options.TracerType,
				"serviceName":  options.ServiceName,
				"collectorURL": options.CollectorURL,
				"sampleRate":   options.SampleRate,
				"tags":         strings.StringMap(options.Tags).String(),
			},
		)
	}

	switch options.TracerType {
	case types.TracerTypeStdout.String():
		logTracerRegistration()
		return stdout.NewTracer(options)
	case types.TracerTypeJaeger.String():
		logTracerRegistration()
		return jaeger.NewTracer(options)
	case types.TracerTypeZipkin.String():
		logTracerRegistration()
		return zipkin.NewTracer(options)
	}

	return noop.NewTracer(options)
}
