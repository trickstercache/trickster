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

package prometheus

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
)

// globalQuery is an instant or range query's parameters, validated before any cache lookup or raw read
type globalQuery struct {
	query   string
	isRange bool
	// at is an instant query's time, zero for now; start, end and step bound a range query
	at         time.Time
	start, end time.Time
	step       time.Duration
	// timeout is zero, and partial nil, when the request leaves the ALB's setting
	timeout time.Duration
	partial *bool
	limit   int
}

// globalRoute returns the evaluator's path for the query r asks, whatever prefix reached it, or ""
func globalRoute(r *http.Request) string {
	switch {
	case strings.HasSuffix(r.URL.Path, APIPath+mnQueryRange):
		return APIPath + mnQueryRange
	case strings.HasSuffix(r.URL.Path, APIPath+mnQuery):
		return APIPath + mnQuery
	}
	return ""
}

// parseGlobalQuery validates r's parameters as Prometheus does, but holds a step to a whole number of
// milliseconds, the engine's resolution, so no step evaluates as another
func parseGlobalQuery(r *http.Request) (globalQuery, error) {
	qp, _, _ := params.GetRequestValues(r)
	q := globalQuery{query: qp.Get(upQuery), isRange: globalRoute(r) == APIPath+mnQueryRange}
	if q.query == "" {
		return q, fmt.Errorf("%w: %s is required", errGlobalBadData, upQuery)
	}
	var err error
	if q.isRange {
		if q.start, err = parseTime(qp.Get(upStart)); err != nil {
			return q, fmt.Errorf("%w: %s: %w", errGlobalBadData, upStart, err)
		}
		if q.end, err = parseTime(qp.Get(upEnd)); err != nil {
			return q, fmt.Errorf("%w: %s: %w", errGlobalBadData, upEnd, err)
		}
		if q.step, err = parseDuration(qp.Get(upStep)); err != nil {
			return q, fmt.Errorf("%w: %s: %w", errGlobalBadData, upStep, err)
		}
		if q.step%time.Millisecond != 0 {
			return q, fmt.Errorf("%w: %s must be a whole number of milliseconds", errGlobalBadData, upStep)
		}
		if q.end.Before(q.start) {
			return q, fmt.Errorf("%w: end timestamp must not be before start time", errGlobalBadData)
		}
		if q.end.Sub(q.start)/q.step > maxGlobalRangePoints {
			return q, fmt.Errorf("%w: exceeded maximum resolution of %d points per timeseries", errGlobalBadData,
				maxGlobalRangePoints)
		}
	} else if v := qp.Get(upTime); v != "" {
		if q.at, err = parseTime(v); err != nil {
			return q, fmt.Errorf("%w: %s: %w", errGlobalBadData, upTime, err)
		}
	}
	if v := qp.Get(timeoutParam); v != "" {
		if q.timeout, err = parseDuration(v); err != nil {
			return q, fmt.Errorf("%w: %s: %w", errGlobalBadData, timeoutParam, err)
		}
	}
	if v := qp.Get(partialResponseParam); v != "" {
		partial, err := strconv.ParseBool(v)
		if err != nil {
			return q, fmt.Errorf("%w: %s: %w", errGlobalBadData, partialResponseParam, err)
		}
		q.partial = &partial
	}
	if v := qp.Get(upLimit); v != "" {
		if q.limit, err = strconv.Atoi(v); err != nil || q.limit < 0 {
			return q, fmt.Errorf("%w: %s must be a non-negative integer", errGlobalBadData, upLimit)
		}
	}
	return q, nil
}

// last returns the latest time the query's result covers, or zero for now
func (q globalQuery) last() time.Time {
	if q.isRange {
		return q.end
	}
	return q.at
}
