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

package aftership

import (
	"errors"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"

	chast "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// ResolveOutputFormat applies a request's default format only when the analyzed
// ClickHouse query has no explicit FORMAT clause.
func ResolveOutputFormat(plan *sqlanalyzer.QueryPlan, fallback string) (byte, error) {
	if plan == nil {
		return 0, ErrNotTimeRangeQuery
	}
	renderer, ok := plan.Renderer.(*clickHouseRenderer)
	if fallback == "" || !ok || renderer.explicitFormat {
		return plan.OutputFormat, nil
	}
	output, ok := supportedFormats[strings.ToLower(fallback)]
	if !ok {
		return 0, ErrUnsupportedOutputFormat
	}
	return output, nil
}

// SplitFormat renders a SELECT without its top-level FORMAT clause.
func SplitFormat(statement, fallback string) (string, string, bool, error) {
	statements, err := chast.NewParser(statement).ParseStmts()
	if err != nil {
		return "", "", false, err
	}
	if len(statements) != 1 {
		return "", "", false, errors.New("expected one SQL statement")
	}
	switch statements[0].(type) {
	case *chast.UseStmt, *chast.SetStmt:
		return "", "", false, errors.New("session-changing SQL is not supported; use database and per-query settings")
	case *chast.InsertStmt:
		return "", "", false, errors.New("INSERT queries are not supported by the native proxy path")
	}
	query, ok := statements[0].(*chast.SelectQuery)
	if !ok {
		return statement, fallback, false, nil
	}
	if query.Format != nil && query.Format.Format != nil {
		fallback = query.Format.Format.Name
		query.Format = nil
	}
	return chast.Format(query), fallback, true, nil
}
