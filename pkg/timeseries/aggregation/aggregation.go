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

package aggregation

type (
	Operator  = string
	Operators []Operator
)

const (
	// Common time-series aggregations.
	Sum         Operator = "sum"
	Count       Operator = "count"
	CountValues Operator = "count_values"
	Average     Operator = "avg"
	Minimum     Operator = "min"
	Maximum     Operator = "max"
	Group       Operator = "group"

	// PromQL-specific aggregations.
	TopK       Operator = "topk"
	BottomK    Operator = "bottomk"
	StdDev     Operator = "stddev"
	StdVar     Operator = "stdvar"
	Quantile   Operator = "quantile"
	LimitK     Operator = "limitk"
	LimitRatio Operator = "limit_ratio"
)
