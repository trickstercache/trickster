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

package cockroach

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
)

func TestEpochFloorMatcher(t *testing.T) {
	for _, sql := range []string{
		"floor(extract(epoch FROM ts)/300)*300", "300*floor(date_part('epoch', ts)/300)",
		"(floor((ts)/300)*300)", "floor(extract(epoch FROM (ts))/300)*300",
	} {
		t.Run(sql, func(t *testing.T) {
			expr, err := parser.ParseExpr(sql)
			if err != nil {
				t.Fatal(err)
			}
			match, ok := EpochFloorMatcher(expr)
			if !ok || match.TimeColumn != "ts" || match.Step != 5*time.Minute || match.OutputUnit != timeseries.DateTimeUnixSecs || match.OutputColumn != "" {
				t.Fatalf("got %+v/%t", match, ok)
			}
		})
	}
	for _, sql := range []string{
		"ts", "floor(ts/300)+300", "floor(ts/300)*width", "floor(ts/300)*0", "floor(ts/300)*-1",
		"floor(ts/300)*0.5", "ceil(ts/300)*300", "floor(ts)*300", "floor(ts+300)*300",
		"floor(ts/300)*600", "floor(ts/9223372036854775807)*9223372036854775807",
		"floor(extract(day FROM ts)/300)*300", "floor(date_part(1, ts)/300)*300",
		"floor(date_part('epoch', ts + 1)/300)*300", "floor(other(ts)/300)*300", "floor(date_part('epoch')/300)*300",
		"floor(DISTINCT ts/300)*300", "floor(ts/300) FILTER (WHERE ts > 0)*300", "floor(ts/300) OVER ()*300",
	} {
		t.Run(sql, func(t *testing.T) {
			expr, err := parser.ParseExpr(sql)
			if err != nil {
				t.Fatal(err)
			}
			if match, ok := EpochFloorMatcher(expr); ok {
				t.Fatalf("accepted %+v", match)
			}
		})
	}
}
