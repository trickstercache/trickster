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
)

// TestParseStatementMixedStatements is a regression test for the nil-pointer
// panic on statements mixing SELECT with non-SELECT (SHOW ...) queries.
func TestParseStatementMixedStatements(t *testing.T) {
	trq, canOPC, err := ParseStatement(
		"SELECT mean(v) FROM m WHERE time >= now() - 1h GROUP BY time(1m) ; SHOW DATABASES",
		time.Now())
	if err == nil {
		t.Fatal("expected a cache error for mixed statements")
	}
	if trq == nil {
		t.Fatal("expected a TimeRangeQuery for parseable mixed statements")
	}
	if !canOPC {
		t.Error("expected object-cache eligibility from the SELECT statement")
	}
	if !strings.Contains(trq.Statement, "SHOW DATABASES") {
		t.Errorf("non-SELECT statement dropped from tokenized statement: %s", trq.Statement)
	}
}

// TestParseStatementTagDimensions verifies GROUP BY tag names surface as tag
// field definitions so grouped results partition into per-tag series.
func TestParseStatementTagDimensions(t *testing.T) {
	trq, canOPC, err := ParseStatement(
		`SELECT mean(v) FROM m WHERE time >= now() - 1h GROUP BY time(1m), "host", region`,
		time.Now())
	if err != nil || !canOPC {
		t.Fatalf("ParseStatement() = %v, canOPC=%t", err, canOPC)
	}
	if len(trq.TagFieldDefintions) != 2 ||
		trq.TagFieldDefintions[0].Name != "host" ||
		trq.TagFieldDefintions[1].Name != "region" {
		t.Fatalf("tag fields = %+v", trq.TagFieldDefintions)
	}
}
