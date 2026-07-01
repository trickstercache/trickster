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

package engines

import (
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func setResourceSpanAttributes(rsc *request.Resources, span trace.Span) {
	if rsc == nil {
		return
	}
	tspan.SetAttributes(rsc.Tracer, span, rsc.TracingAttributes()...)
}

func setHTTPStatusSpanAttributes(tr *tracing.Tracer, statusCode int, spans ...trace.Span) {
	attr := attribute.Int("http.status_code", statusCode)
	for _, span := range spans {
		tspan.SetAttributes(tr, span, attr)
	}
}
