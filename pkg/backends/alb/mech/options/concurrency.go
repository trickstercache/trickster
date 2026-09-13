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

package options

import "runtime"

// ConcurrencyOptions controls query concurrency for ALB mechanisms.
type ConcurrencyOptions struct {
	// QueryConcurrencyLimit defaults to GOMAXPROCS. Zero disables the limit.
	QueryConcurrencyLimit *int `yaml:"query_concurrency_limit,omitempty"`
	// QueryConcurrencyMultiplier scales the limit; values below 1 are treated as 1.
	QueryConcurrencyMultiplier *int `yaml:"query_concurrency_multiplier,omitempty"`
}

// GetQueryConcurrencyLimit returns the effective limit, with zero meaning unlimited.
func (o *ConcurrencyOptions) GetQueryConcurrencyLimit() int {
	multiplier := 1
	if o != nil && o.QueryConcurrencyMultiplier != nil && *o.QueryConcurrencyMultiplier > 1 {
		multiplier = *o.QueryConcurrencyMultiplier
	}
	limit := runtime.GOMAXPROCS(0)
	if o != nil && o.QueryConcurrencyLimit != nil {
		limit = *o.QueryConcurrencyLimit
	}
	return max(limit*multiplier, 0)
}
