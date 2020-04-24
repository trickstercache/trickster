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
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
	"github.com/tricksterproxy/trickster/pkg/util/tracing"
	to "github.com/tricksterproxy/trickster/pkg/util/tracing/options"

	"go.opentelemetry.io/otel/api/trace"
)

// Flushers represents a slice of Flusher functions for the configured Tracers
type Flushers []func()

// RegisterAll registers all Tracers in the provided configuration, and returns
// their Flushers
func RegisterAll(cfg *config.Config, log *tl.Logger) (Flushers, error) {

	if cfg == nil {
		return nil, errors.New("no config provided")
	}

	if cfg.Origins == nil {
		return nil, errors.New("no origins provided")
	}

	flushers := make(Flushers, 0, len(cfg.Origins))
	activeTracers := make(map[string]*to.Options)

	for _, oc := range cfg.Origins {
		if oc != nil {

			tc := oc.TracingConfig
			if tc == nil {
				continue
			}

			if tc.Implementation == "" {
				continue
			}

			if _, ok := tracing.TracerImplementations[tc.Implementation]; !ok {
				return nil, fmt.Errorf("invalid tracing implementation [%s] for tracing config [%s]", tc.Implementation, oc.TracingConfigName)
			}

			if _, ok := activeTracers[oc.TracingConfigName]; !ok {
				tracer, flusher, err := Init(tc, log)
				if err != nil {
					return nil, err
				}
				flushers = append(flushers, flusher)
				tc.Tracer = tracer
				tc.Name = oc.TracingConfigName
				activeTracers[oc.TracingConfigName] = tc
			}
		}
	}
	return flushers, nil
}

// Init initializes tracing and returns a function to flush the tracer. Flush should be called on server shutdown.
func Init(cfg *to.Options, log *tl.Logger) (trace.Tracer, func(), error) {

	if cfg == nil {
		log.Info(
			"nil tracing config, using noop tracer", nil,
		)
		return trace.NoopTracer{}, func() {}, nil
	}
	log.Debug(
		"Trace Init",
		tl.Pairs{
			"Implementation": cfg.Implementation,
			"Collector":      cfg.Exporter.Collector,
			"Type":           tracing.TracerImplementations[cfg.Implementation],
		},
	)

	tracer, flusher, _, err := tracing.SetTracer(
		tracing.TracerImplementations[cfg.Implementation],
		tracing.TraceExporters[cfg.Exporter.Exporter],
		tracing.WithSampleRate(cfg.Exporter.SampleRate),
		tracing.WithCollector(cfg.Exporter.Collector),
		tracing.WithAgent(cfg.Exporter.Agent),
	)
	return tracer, flusher, err
}
