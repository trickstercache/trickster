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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/aftership"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

var dialectAnalyzer sqlanalyzer.DialectAnalyzer = aftership.NewAnalyzer()

func parse(
	statement string,
	observe func(sqlanalyzer.Analysis),
) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	now := time.Now()
	analysis := dialectAnalyzer.Analyze(statement, now)
	if observe != nil {
		observe(analysis)
	}
	return parseAnalysis(statement, now, analysis)
}

func parseAnalysis(
	statement string,
	now time.Time,
	analysis sqlanalyzer.Analysis,
) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	canObjectCache := analysis.Mode >= sqlanalyzer.CacheModeObject
	trq := sqlanalyzer.NewTimeRangeQuery(statement)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		if !canObjectCache {
			return nil, nil, false, analysis.Err
		}
		return trq, nil, true, analysis.Err
	}

	plan := analysis.Plan
	plan.ApplyToQuery(trq)
	trq.Extent = plan.RequestExtent(now)
	trq.ExtractBackfillTolerance(statement)

	options := &timeseries.RequestOptions{
		OutputFormat:           plan.OutputFormat,
		BaseTimestampFieldName: plan.TimeColumn,
	}
	options.ExtractFastForwardDisabled(statement)
	return trq, options, true, nil
}
