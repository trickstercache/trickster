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

package greptimedb

import (
	"errors"
	"net/http"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

var (
	_            backends.TSMMergeProvider = (*Client)(nil)
	metricParser                           = parser.NewParser(parser.Options{EnableExperimentalFunctions: true})
)

// PlanTSMMerge normalizes Greptime's URL-first semantics before the shared
// planner chooses or rewrites any expression. Member handlers run too late.
func (c *Client) PlanTSMMerge(r *http.Request, _ string) (*merge.TSMMergePlan, error) {
	req, err := request.Clone(r)
	if err != nil {
		return nil, err
	}
	if req == nil || !preparePromRequest(req) {
		return nil, errors.New("unsupported GreptimeDB PromQL merge request")
	}
	values, _, _ := params.GetRequestValues(req)
	query := values.Get("query")
	plan, err := c.Client.PlanTSMMerge(req, query)
	if err != nil {
		return nil, err
	}
	if expr, err := promql.Parse(query); err == nil && expr.ContainsAggregation() {
		plan.StripInjectedLabels = true
	}
	if plan.Finalizer.Enabled {
		if _, known := greptimeMetricName(query); !known {
			return nil, errors.New("cannot finalize GreptimeDB metric-name discovery across shards")
		}
	}
	return plan, nil
}

// FinalizeTSMMerge reuses the shared numeric reducers while preserving the
// metric-name contract of Greptime's HTTP response builder.
func (c *Client) FinalizeTSMMerge(query string, ts timeseries.Timeseries) {
	c.Client.FinalizeTSMMerge(query, ts)
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		return
	}
	name, known := greptimeMetricName(query)
	if !known {
		return
	}
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			if name == "" {
				delete(series.Header.Tags, promql.MetricNameLabel)
			} else {
				if series.Header.Tags == nil {
					series.Header.Tags = make(dataset.Tags)
				}
				series.Header.Tags[promql.MetricNameLabel] = name
			}
			series.Header.Name = name
			series.Header.CalculateHash(true)
			series.Header.CalculateSize()
		}
	}
}

// Greptime's servers/src/http/prometheus.rs collects metric names through
// aggregates/functions, but clears them for arithmetic and grouping that
// excludes __name__. Regex discovery is expanded upstream and is not static.
func greptimeMetricName(query string) (string, bool) {
	expr, err := metricParser.ParseExpr(query)
	if err != nil {
		return "", false
	}
	static := true
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if !ok || selector.Name != "" {
			return nil
		}
		literal := false
		for _, matcher := range selector.LabelMatchers {
			if matcher.Name == promql.MetricNameLabel && matcher.Type == labels.MatchEqual {
				literal = true
			}
		}
		static = static && literal
		return nil
	})
	if !static {
		return "", false
	}
	names := make(map[string]struct{})
	var collect func(parser.Expr) bool
	collect = func(expr parser.Expr) bool {
		switch e := expr.(type) {
		case *parser.AggregateExpr:
			if (e.Without && slices.Contains(e.Grouping, promql.MetricNameLabel)) ||
				(!e.Without && e.Grouping != nil && !slices.Contains(e.Grouping, promql.MetricNameLabel)) {
				clear(names)
				return true
			}
			return collect(e.Expr)
		case *parser.UnaryExpr:
			clear(names)
		case *parser.BinaryExpr:
			if e.Op == parser.LAND || e.Op == parser.LOR || e.Op == parser.LUNLESS {
				return collect(e.LHS)
			}
			clear(names)
		case *parser.ParenExpr:
			return collect(e.Expr)
		case *parser.SubqueryExpr:
			return collect(e.Expr)
		case *parser.MatrixSelector:
			return collect(e.VectorSelector)
		case *parser.VectorSelector:
			if e.Name != "" {
				names[e.Name] = struct{}{}
				return true
			}
			for _, m := range e.LabelMatchers {
				if m.Name != promql.MetricNameLabel {
					continue
				}
				if m.Type != labels.MatchEqual {
					return false
				}
				names[m.Value] = struct{}{}
				return true
			}
		case *parser.Call:
			for _, arg := range e.Args {
				if !collect(arg) {
					return false
				}
			}
		}
		return true
	}
	if !collect(expr) {
		return "", false
	}
	if len(names) == 1 {
		for name := range names {
			return name, true
		}
	}
	return "", true
}
