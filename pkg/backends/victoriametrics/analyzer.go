/*
 * Copyright 2026 The Trickster Authors
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

package victoriametrics

import (
	"hash/maphash"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/keytable"

	"github.com/VictoriaMetrics/metricsql"
)

// cacheMode is how a MetricsQL expression may be cached.
type cacheMode uint8

const (
	// modeDelta expressions evaluate each point from data near it, so ranges fetched apart merge
	modeDelta cacheMode = iota
	// modeObject expressions depend on the whole requested range, so only exact requests are cached
	modeObject
	// modeProxy expressions are relayed uncached: volatile functions and unparsable statements
	modeProxy
)

// analysis is the classification of one MetricsQL statement.
type analysis struct {
	query  string
	mode   cacheMode
	reason string
}

const (
	reasonParse      = "parse_error"
	reasonVolatile   = "volatile_function"
	reasonRangeFunc  = "range_function"
	reasonRangeAggr  = "range_aggregate"
	reasonLimit      = "limit_modifier"
	reasonEvaluateAt = "at_modifier"
	analysisEntries  = 4096
	analysisIdle     = 30 * time.Minute
)

// volatileFuncs differ on every evaluation, so no response holding them can be reused.
var volatileFuncs = map[string]struct{}{
	"now": {}, "rand": {}, "rand_exponential": {}, "rand_normal": {},
}

// rangeTransforms use samples or series across the whole requested range, so a range assembled
// from separate fetches differs from one evaluation of it. start and end name the range itself.
var rangeTransforms = map[string]struct{}{
	"buckets_limit": {}, "drop_common_labels": {}, "drop_empty_series": {}, "end": {},
	"interpolate": {}, "keep_last_value": {}, "keep_next_value": {}, "limit_offset": {},
	"range_avg": {}, "range_first": {}, "range_last": {}, "range_linear_regression": {},
	"range_mad": {}, "range_max": {}, "range_min": {}, "range_normalize": {}, "range_quantile": {},
	"range_stddev": {}, "range_stdvar": {}, "range_sum": {}, "range_trim_outliers": {},
	"range_trim_spikes": {}, "range_trim_zscore": {}, "range_zscore": {}, "remove_resets": {},
	"running_avg": {}, "running_max": {}, "running_min": {}, "running_sum": {},
	"smooth_exponential": {}, "sort": {}, "sort_by_label": {}, "sort_by_label_desc": {},
	"sort_by_label_numeric": {}, "sort_by_label_numeric_desc": {}, "sort_desc": {},
	"start": {}, "union": {},
}

// rangeAggrs choose series by values or membership across the whole requested range.
var rangeAggrs = map[string]struct{}{
	"any": {}, "bottomk_avg": {}, "bottomk_last": {}, "bottomk_max": {}, "bottomk_median": {},
	"bottomk_min": {}, "limitk": {}, "outliers_iqr": {}, "outliers_mad": {}, "outliersk": {},
	"topk_avg": {}, "topk_last": {}, "topk_max": {}, "topk_median": {}, "topk_min": {},
}

// analyzer classifies MetricsQL statements, remembering recent ones so a dashboard's repeated
// queries are parsed once.
type analyzer struct {
	seed  maphash.Seed
	table *keytable.Table[*analysis]
}

func newAnalyzer() *analyzer {
	return &analyzer{
		seed:  maphash.MakeSeed(),
		table: keytable.New[*analysis](keytable.Options{MaxEntries: analysisEntries, Idle: analysisIdle}),
	}
}

// analyze returns the classification of query, parsing it only when it isn't remembered.
func (a *analyzer) analyze(query string) *analysis {
	key := maphash.String(a.seed, query)
	nowNano := time.Now().UnixNano()
	if v, ok := a.table.Get(key, nowNano); ok && v.query == query {
		return v
	}
	v := classify(query)
	a.table.Put(key, v, nowNano)
	return v
}

// classify parses query and returns the most restrictive mode any of its parts requires.
func classify(query string) *analysis {
	out := &analysis{query: query, mode: modeDelta}
	expr, err := metricsql.Parse(query)
	if err != nil {
		out.mode, out.reason = modeProxy, reasonParse
		return out
	}
	restrict := func(m cacheMode, reason string) {
		if m > out.mode {
			out.mode, out.reason = m, reason
		}
	}
	metricsql.VisitAll(expr, func(e metricsql.Expr) {
		switch e := e.(type) {
		case *metricsql.FuncExpr:
			if _, ok := volatileFuncs[e.Name]; ok {
				restrict(modeProxy, reasonVolatile)
			} else if _, ok := rangeTransforms[e.Name]; ok {
				restrict(modeObject, reasonRangeFunc)
			}
		case *metricsql.AggrFuncExpr:
			if _, ok := rangeAggrs[e.Name]; ok {
				restrict(modeObject, reasonRangeAggr)
			}
			if e.Limit > 0 {
				restrict(modeObject, reasonLimit)
			}
		case *metricsql.RollupExpr:
			// @ with a fixed timestamp evaluates the same everywhere; any other @ depends on the range
			if e.At != nil {
				if _, ok := e.At.(*metricsql.NumberExpr); !ok {
					restrict(modeObject, reasonEvaluateAt)
				}
			}
		}
	})
	return out
}
