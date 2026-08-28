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

package clickhouse

import (
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
)

const clickHouseDialect = "clickhouse"

func (c *Client) observeAnalysis(analysis sqlanalyzer.Analysis) {
	backendName := c.observabilityBackendName()
	reason := string(analysis.Reason)
	if reason == "" {
		reason = "unknown"
	}
	mode := analysis.Mode.String()
	metrics.SQLQueryAnalysis.WithLabelValues(backendName, clickHouseDialect, mode, reason).Inc()
	if c != nil && c.TimeseriesBackend != nil && analysis.Mode == sqlanalyzer.CacheModeObject &&
		analysis.Reason != sqlanalyzer.ReasonUnsupportedBucket && analysis.Reason != sqlanalyzer.ReasonNotTimeRange {
		options := c.Configuration()
		if options != nil && (options.DPCFallbackWarning == nil || *options.DPCFallbackWarning) {
			logger.Warn("query fell back from DPC to OPC", logging.Pairs{
				"backend_name": backendName, "dialect": clickHouseDialect, "reason": reason,
			})
		}
	}
	if logger.Level() == level.Debug {
		logger.Debug("sql query cache eligibility analyzed", logging.Pairs{
			"backend_name": backendName,
			"dialect":      clickHouseDialect,
			"cache_mode":   mode,
			"reason":       reason,
		})
	}
}

func (c *Client) observeRewriteFailure(reason string) {
	backendName := c.observabilityBackendName()
	metrics.SQLQueryRewriteFailures.WithLabelValues(backendName, clickHouseDialect, reason).Inc()
	logger.Error("sql query extent rewrite failed", logging.Pairs{
		"backend_name": backendName,
		"dialect":      clickHouseDialect,
		"reason":       reason,
	})
}

func (c *Client) observabilityBackendName() string {
	if c == nil || c.TimeseriesBackend == nil {
		return ""
	}
	return c.Name()
}
