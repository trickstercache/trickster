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

package druid

import (
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

type (
	analysisMode   string
	analysisReason string
)

const (
	modeDelta  analysisMode = "delta"
	modeObject analysisMode = "object"
	modeProxy  analysisMode = "proxy"

	reasonEligible               analysisReason = "eligible"
	reasonUnsupportedMethod      analysisReason = "unsupported_method"
	reasonUnsupportedContentType analysisReason = "unsupported_content_type"
	reasonInvalidJSON            analysisReason = "invalid_json"
	reasonUnsupportedQueryType   analysisReason = "unsupported_query_type"
	reasonInvalidContext         analysisReason = "invalid_context"
	reasonInvalidInterval        analysisReason = "invalid_interval"
	reasonMultipleIntervals      analysisReason = "multiple_intervals"
	reasonUnsupportedGranularity analysisReason = "unsupported_granularity"
	reasonNonFixedGranularity    analysisReason = "non_fixed_granularity"
	reasonUnsupportedShape       analysisReason = "unsupported_response_shape"
	reasonUnsupportedDimension   analysisReason = "unsupported_dimension"
)

func (c *Client) observeAnalysis(mode analysisMode, reason analysisReason) {
	backendName := c.observabilityBackendName()
	metrics.DruidQueryAnalysis.WithLabelValues(backendName, string(mode), string(reason)).Inc()
	if logger.Level() == level.Debug {
		logger.Debug("Druid query cache eligibility analyzed", logging.Pairs{
			keys.BackendName: backendName,
			keys.CacheMode:   string(mode),
			keys.Reason:      string(reason),
		})
	}
}

func (c *Client) observeRewriteFailure(reason string) {
	backendName := c.observabilityBackendName()
	metrics.DruidQueryRewriteFailures.WithLabelValues(backendName, reason).Inc()
	logger.Error("Druid query extent rewrite failed", logging.Pairs{
		keys.BackendName: backendName,
		keys.Reason:      reason,
	})
}

func (c *Client) observabilityBackendName() string {
	if c == nil || c.TimeseriesBackend == nil {
		return ""
	}
	return c.Name()
}
