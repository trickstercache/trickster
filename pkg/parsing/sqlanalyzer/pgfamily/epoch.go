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

package pgfamily

import (
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// EpochFloorMatcher matches floor(extract(epoch FROM column)/N)*N, date_part
// and raw epoch-second variants. The result is also an epoch-second count.
// OutputColumn is left empty because unaliased expression names are dialect-specific.
func EpochFloorMatcher(expr ast.Expr) (BucketMatch, bool) {
	none := BucketMatch{}
	product, ok := unwrapParens(expr).(*ast.BinaryExpr)
	if !ok || product.Operator.Symbol != ast.Mult {
		return none, false
	}
	floored, multiplier := product.Left, product.Right
	seconds, ok := positiveInteger(multiplier)
	if !ok {
		floored, multiplier = multiplier, floored
		if seconds, ok = positiveInteger(multiplier); !ok {
			return none, false
		}
	}
	floor, ok := unwrapParens(floored).(*ast.FuncExpr)
	if !ok || len(floor.Exprs) != 1 || !plainCall(floor, "floor") {
		return none, false
	}
	quotient, ok := unwrapParens(floor.Exprs[0]).(*ast.BinaryExpr)
	if !ok || quotient.Operator.Symbol != ast.Div {
		return none, false
	}
	if divisor, ok := positiveInteger(quotient.Right); !ok || divisor != seconds ||
		seconds > int64((1<<63-1)/time.Second) {
		return none, false
	}
	match := BucketMatch{Step: time.Duration(seconds) * time.Second, OutputUnit: timeseries.DateTimeUnixSecs}
	source := unwrapParens(quotient.Left)
	if column, ok := ColumnName(source); ok {
		match.TimeColumn, match.ColumnUnit = column, timeseries.DateTimeUnixSecs
		return match, true
	}
	epoch, ok := source.(*ast.FuncExpr)
	if !ok || len(epoch.Exprs) != 2 || !plainCall(epoch, "extract") && !plainCall(epoch, "date_part") {
		return none, false
	}
	if field, ok := epoch.Exprs[0].(*ast.StrVal); !ok || !strings.EqualFold(field.RawString(), "epoch") {
		return none, false
	}
	if match.TimeColumn, ok = ColumnName(unwrapParens(epoch.Exprs[1])); !ok {
		return none, false
	}
	return match, true
}

func plainCall(function *ast.FuncExpr, name string) bool {
	return function.Filter == nil && function.WindowDef == nil && len(function.OrderBy) == 0 &&
		function.Type == 0 && strings.EqualFold(function.Func.Name(), name)
}

func positiveInteger(expr ast.Expr) (int64, bool) {
	number, ok := unwrapParens(expr).(*ast.NumVal)
	if !ok {
		return 0, false
	}
	value, err := number.AsInt64()
	return value, err == nil && value > 0
}
