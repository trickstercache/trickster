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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
)

func TestResolveOutputFormat(t *testing.T) {
	const query = "SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= 120 AND ts < 240 GROUP BY t"
	for _, test := range []struct {
		name, clause, fallback string
		want                   byte
		wantErr                error
	}{
		{name: "default", want: 3},
		{name: "native fallback", fallback: "nAtIvE", want: 6},
		{name: "JSON fallback", fallback: "JSON", want: 0},
		{name: "explicit overrides fallback", clause: " FORMAT CSV", fallback: "Native", want: 1},
		{name: "explicit ignores invalid fallback", clause: " FORMAT JSON", fallback: "Parquet", want: 0},
		{name: "invalid fallback", fallback: "Parquet", wantErr: ErrUnsupportedOutputFormat},
	} {
		t.Run(test.name, func(t *testing.T) {
			analysis := NewAnalyzer().Analyze(query+test.clause, time.Unix(500, 0))
			if analysis.Err != nil {
				t.Fatal(analysis.Err)
			}
			originalFormat := analysis.Plan.OutputFormat
			got, err := ResolveOutputFormat(analysis.Plan, test.fallback)
			if got != test.want || !errors.Is(err, test.wantErr) {
				t.Fatalf("format = %d, err = %v; want %d, %v", got, err, test.want, test.wantErr)
			}
			if analysis.Plan.OutputFormat != originalFormat {
				t.Fatal("format resolution mutated the query plan")
			}
		})
	}
	if _, err := ResolveOutputFormat(nil, "JSON"); !errors.Is(err, ErrNotTimeRangeQuery) {
		t.Fatalf("nil plan: %v", err)
	}
	if got, err := ResolveOutputFormat(&sqlanalyzer.QueryPlan{OutputFormat: 2}, "Native"); got != 2 || err != nil {
		t.Fatalf("foreign plan: format = %d, err = %v", got, err)
	}
}

func TestSplit(t *testing.T) {
	for _, test := range []struct {
		sql, want, format string
		selectQuery       bool
	}{
		{"SELECT 1 FORMAT Native", "SELECT 1", "Native", true},
		{"SELECT 1; -- trailing comment", "SELECT 1", "JSON", true},
		{"SELECT 'FORMAT Native'", "SELECT 'FORMAT Native'", "JSON", true},
		{"CREATE TABLE t (id UInt64) ENGINE = Memory", "CREATE TABLE t (id UInt64) ENGINE = Memory", "JSON", false},
	} {
		sql, format, sel, err := SplitFormat(test.sql, "JSON")
		if err != nil || sql != test.want || format != test.format || sel != test.selectQuery {
			t.Fatalf("%s: %q %q %v %v", test.sql, sql, format, sel, err)
		}
	}
	for _, sql := range []string{
		"SELECT 1; SELECT 2", "USE other", "SET max_threads=2", "INSERT INTO t VALUES (1)", "",
	} {
		if _, _, _, err := SplitFormat(sql, "JSON"); err == nil {
			t.Fatalf("accepted %q", sql)
		}
	}
}
