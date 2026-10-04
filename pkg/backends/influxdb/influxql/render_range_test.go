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

package influxql

import (
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/influxdata/influxql"
	"github.com/stretchr/testify/require"
)

const (
	rangeSelect = `SELECT mean(v) FROM cpu WHERE host = 'a' AND time >= '2024-01-01T00:00:07Z'`
	rangeGroup  = ` GROUP BY time(1m)`
)

func TestRenderRange(t *testing.T) {
	now := time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC)
	label := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	closed := timeseries.PartialBucket{Lower: label.Add(7 * time.Second), Upper: label.Add(time.Minute)}
	open := timeseries.PartialBucket{Lower: label}
	tests := []struct {
		name, where string
		pb          timeseries.PartialBucket
		want        string
		err         error
	}{
		{
			"closed range", ` AND time < '2024-01-01T01:00:13Z'`, closed,
			`SELECT mean(v) FROM cpu WHERE host = 'a' AND time >= '2024-01-01T00:00:07Z' AND ` +
				`time < '2024-01-01T00:01:00Z' GROUP BY time(1m)`, nil,
		},
		{
			"no upper bound", ``, open,
			`SELECT mean(v) FROM cpu WHERE host = 'a' AND time >= '2024-01-01T00:00:00Z' GROUP BY time(1m)`, nil,
		},
		// a bare now() upper bound is kept as written, so the statement is the same throughout the bucket
		{
			"now() upper bound", ` AND time <= now()`, open,
			`SELECT mean(v) FROM cpu WHERE host = 'a' AND time <= now() AND time >= '2024-01-01T00:00:00Z' ` +
				`GROUP BY time(1m)`, nil,
		},
		{
			"parenthesized now()", ` AND (time < now())`, open,
			`SELECT mean(v) FROM cpu WHERE host = 'a' AND (time < now()) AND time >= '2024-01-01T00:00:00Z' ` +
				`GROUP BY time(1m)`, nil,
		},
		{"exclusive lower", ``, timeseries.PartialBucket{Lower: label, LowerExclusive: true}, "", ErrUnsupportedRange},
		{"inclusive upper", ``, timeseries.PartialBucket{Lower: label, Upper: now, UpperInclusive: true}, "", ErrUnsupportedRange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trq, _, err := ParseStatement(rangeSelect+test.where+rangeGroup, now)
			require.NoError(t, err)
			got, err := RenderRange(trq.ParsedQuery.(*influxql.Query), test.pb)
			require.ErrorIs(t, err, test.err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestRenderRangeStatements(t *testing.T) {
	// every SELECT gets the range, and other statements ride along unchanged
	q, err := influxql.NewParser(stringsReader(`SELECT mean(v) FROM cpu WHERE time >= now() - 1h ` +
		`GROUP BY time(1m); SHOW DATABASES`)).ParseQuery()
	require.NoError(t, err)
	got, err := RenderRange(q, timeseries.PartialBucket{Lower: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)})
	require.NoError(t, err)
	require.Equal(t, "SELECT mean(v) FROM cpu WHERE time >= '2024-01-01T00:00:00Z' GROUP BY time(1m);\n"+
		"SHOW DATABASES", got)
	// a kept now() condition loses its other time bounds, however they're grouped
	trq, _, err := ParseStatement(`SELECT mean(v) FROM cpu WHERE (host = 'a' AND time >= '2024-01-01T00:00:07Z') `+
		`AND time <= now() GROUP BY time(1m)`, time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	got, err = RenderRange(trq.ParsedQuery.(*influxql.Query), timeseries.PartialBucket{Lower: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)})
	require.NoError(t, err)
	require.Equal(t, "SELECT mean(v) FROM cpu WHERE (host = 'a') AND time <= now() AND "+
		"time >= '2024-01-01T00:00:00Z' GROUP BY time(1m)", got)
	// a statement with no condition gets the lower bound alone
	q, err = influxql.NewParser(stringsReader(`SELECT mean(v) FROM cpu GROUP BY time(1m)`)).ParseQuery()
	require.NoError(t, err)
	got, err = RenderRange(q, timeseries.PartialBucket{Lower: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)})
	require.NoError(t, err)
	require.Equal(t, "SELECT mean(v) FROM cpu WHERE time >= '2024-01-01T00:00:00Z' GROUP BY time(1m)", got)
}

func TestParseStatementOpenEnds(t *testing.T) {
	now := time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC)
	for where, open := range map[string]bool{
		``:                                   true,
		` AND time < now()`:                  true,
		` AND time <= now()`:                 true,
		` AND time < now() - 1m`:             false,
		` AND time < '2024-01-01T01:00:13Z'`: false,
		// a literal upper bound before now wins over a now() one
		` AND time < now() AND time < '2024-01-01T01:00:13Z'`: false,
	} {
		trq, _, err := ParseStatement(rangeSelect+where+rangeGroup, now)
		require.NoError(t, err, where)
		require.Equal(t, open, trq.Requested.OpenEnded, where)
		if open {
			require.Equal(t, now, trq.Requested.End, where)
		}
		// the cache key never holds the range
		require.NotContains(t, trq.Statement, "now()", where)
	}
}

func stringsReader(s string) *strings.Reader {
	return strings.NewReader(s)
}
