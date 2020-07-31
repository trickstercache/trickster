/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

// Package sqlparser provides customizations to the base sql parser that are
// specific to timeseries (for example, parsing trickster directives from comments)
package sqlparser

import (
	"context"

	"github.com/tricksterproxy/trickster/pkg/parsing"
	lsql "github.com/tricksterproxy/trickster/pkg/parsing/lex/sql"
	"github.com/tricksterproxy/trickster/pkg/parsing/sql"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

// Parser is a basic, extendable SQL Parser
type Parser struct {
	*sql.Parser
}

// NewRunContext returns a context with the Time Range Query and Request Options attached
func NewRunContext(trq *timeseries.TimeRangeQuery,
	ro *timeseries.RequestOptions) context.Context {
	return context.WithValue(
		context.WithValue(context.Background(), timeseries.TimeRangeQueryCtx, trq),
		timeseries.RequestOptionsCtx, ro)
}

// ArtifactsFromContext returns the Time Range Query and Request Options from the context, if present
func ArtifactsFromContext(ctx context.Context) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions) {
	v := ctx.Value(timeseries.TimeRangeQueryCtx)
	trq, _ := v.(*timeseries.TimeRangeQuery)
	v = ctx.Value(timeseries.RequestOptionsCtx)
	ro, _ := v.(*timeseries.RequestOptions)
	return trq, ro
}

// New returns a new Time Series SQL Parser
func New(po *parsing.Options) parsing.Parser {
	po = po.WithDecisions("FindVerb",
		parsing.DecisionSet{
			lsql.TokenComment: ParseFVComment,
		},
	)
	p := &Parser{
		Parser: sql.New(po).(*sql.Parser),
	}
	return p
}

// ParseComment will parse the comment for Trickster time range query directives
// such as Fast Forward Disable or a Backfill Tolerance value. It assumes the
// RunState is currently on the Comment Token
func ParseComment(rs *parsing.RunState) {
	i := rs.Current()
	trq, ro := ArtifactsFromContext(rs.Context())
	if trq != nil {
		// TimeRangeQuery extractions here
		trq.ExtractBackfillTolerance(i.Val)
	}
	if ro != nil {
		// RequestOption extractions here
		ro.ExtractFastForwardDisabled(i.Val)
	}
}

// ParseFVComment will parse the comment and return FindVerb to be invoked
func ParseFVComment(bp, ip parsing.Parser, rs *parsing.RunState) parsing.StateFn {
	ParseComment(rs)
	return rs.GetReturnFunc(sql.FindVerb, nil, true)
}
