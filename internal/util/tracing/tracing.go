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
	"context"
	"net/http"
	"sync"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
	"go.opentelemetry.io/otel/api/distributedcontext"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/trace"
	"go.opentelemetry.io/otel/plugin/httptrace"
	"go.opentelemetry.io/otel/propagation"
)

const (
	RequestIDKey     = "trickster-internal-id"
	ServiceName      = "trickster"
	TracestateHeader = "tracestate"
	TracerName       = "Comcast"
)

var (
	TraceHeaders = []string{
		propagation.TraceparentHeader,
		propagation.CorrelationContextHeader,
		TracestateHeader,
	}
	initialize sync.Once
)

// Init initializes tracing and returns a function to flush the tracer. Flush should be called on server shutdown.
func Init(cfg *config.TracingConfig) func() {
	if cfg == nil {
		log.Info(
			"Nil Tracing Config, using noop tracer", nil,
		)
		return func() {}
	}
	log.Debug(
		"Trace Init",
		log.Pairs{
			"Implementation": cfg.Implementation,
			"Collector":      cfg.CollectorEndpoint,
			"Type":           TracerImplementations[cfg.Implementation],
		},
	)
	var flusher func()
	f := func() {
		fl, err := SetTracer(
			TracerImplementations[cfg.Implementation],
			cfg.CollectorEndpoint,
			cfg.SampleRate,
		)
		if err != nil {
			log.Error(
				"Cannot initialize tracing",
				log.Pairs{
					"Error":     err,
					"Tracer":    cfg.Implementation,
					"Collector": cfg.CollectorEndpoint,
				},
			)
		}
		flusher = fl

	}
	initialize.Do(f)
	return flusher
}

// PrepareRequest extracts trace information from the headers of the incoming request. It returns a pointer to the incoming request with the request context updated to include all span and tracing info. It also returns a span with the name "Request" that is meant to be a parent span for all child spans of this request.
func PrepareRequest(r *http.Request, tracerName string) (*http.Request, trace.Span) {

	attrs, entries, spanCtx := httptrace.Extract(r.Context(), r)

	ctx := distributedcontext.WithMap(
		r.Context(),
		distributedcontext.NewMap(
			distributedcontext.MapUpdate{
				MultiKV: entries,
			},
		),
	)
	ctx = context.WithValue(ctx, spanCtxKey, spanCtx)
	ctx = context.WithValue(ctx, attrKey, attrs)
	ctx = context.WithValue(ctx, tracerNameKey, tracerName)

	tr := global.TraceProvider().Tracer(tracerName)

	ctx, span := tr.Start(
		ctx,
		"Request",
		trace.WithAttributes(attrs...),
		trace.ChildOf(spanCtx),
	)

	return r.WithContext(ctx), span
}
