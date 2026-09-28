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
	"context"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const spanAttrStepAlignment = "step_alignment.mode"

func resolveStepAlignment(ctx context.Context, o *bo.Options, trq *timeseries.TimeRangeQuery,
	tr *tracing.Tracer, span trace.Span,
) {
	if unsupported := trq.ResolveStepAlignment(tctx.StepAlignment(ctx), o.StepAlignment); unsupported != 0 {
		metrics.StepAlignmentFallbacks.WithLabelValues(o.Name, unsupported.String(),
			trq.StepAlignment.String()).Inc()
		logger.Debug("step alignment mode is not supported by the query; using its default",
			logging.Pairs{
				keys.BackendName: o.Name, keys.Requested: unsupported.String(),
				keys.Applied: trq.StepAlignment.String(),
			})
	}
	if trq.StepAlignment != 0 {
		tspan.SetAttributes(tr, span, attribute.String(spanAttrStepAlignment, trq.StepAlignment.String()))
	}
}
