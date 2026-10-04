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

package pgwire

import (
	"testing"
)

func TestGateNeverCachesWhatItCannotReadAsOneRead(t *testing.T) {
	// the gate reads statements lexically while the origin parses them for real; wherever
	// the two could disagree about what a message does, the message must not be cached
	for sql, want := range map[string]struct {
		eligible    bool
		sessionSafe bool
	}{
		// separators and keywords inside quotes and comments are text
		"SELECT ';' FROM t":               {true, true},
		"SELECT $$;$$ FROM t":             {true, true},
		"SELECT $a$ ; DELETE $a$ FROM t":  {true, true},
		"SELECT 1 /* ; DELETE */ FROM t":  {true, true},
		"SELECT 1 FROM t -- ; DELETE":     {true, true},
		`SELECT "a;b", "delete" FROM t`:   {true, true},
		"SELECT E'\\';DELETE' FROM t":     {true, true},
		"SELECT 1 FROM t; -- done":        {true, true},
		"select 1 from t where a = 'x';;": {true, true},
		"TABLE t":                         {true, true},
		// more than one statement, or not a read at all
		"SELECT 1;SELECT 2":          {false, true},
		"/* SELECT */ DELETE FROM t": {false, true},
		"EXPLAIN ANALYZE SELECT 1":   {false, true},
		"COPY t TO STDOUT":           {false, true},
		// reads that write: a cached answer would skip the write the next time
		"WITH gone AS (DELETE FROM t RETURNING *) SELECT * FROM gone":                    {false, true},
		"WITH n AS (INSERT INTO t VALUES (1) RETURNING *) SELECT * FROM n":               {false, true},
		"WITH u AS (UPDATE t SET a = 1 RETURNING *) SELECT count(*) FROM u":              {false, true},
		"WITH m AS (MERGE INTO t USING s ON true WHEN MATCHED THEN DO NOTHING) SELECT 1": {false, true},
		"SELECT * FROM t FOR UPDATE":                                                     {false, true},
		"SELECT * FROM t FOR NO KEY update":                                              {false, true},
		// reads that change what later statements mean
		"SELECT * INTO copy FROM t":                          {false, false},
		"SELECT 1; SELECT * INTO copy FROM t":                {false, false},
		"SELECT set_config('app.tenant', '1', false)":        {false, false},
		"WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x": {false, false},
		"PREPARE p AS SELECT 1":                              {false, false},
		"EXECUTE p":                                          {false, false},
		"SELECT 'unterminated":                               {false, false},
	} {
		s := gateTestSession(t, nil)
		outcome := s.gateQuery([]byte(sql + "\x00"))
		if safe, _ := s.tracker.cacheable(); outcome.eligible != want.eligible || safe != want.sessionSafe {
			t.Fatalf("%q: eligible=%t session safe=%t, want %t/%t", sql, outcome.eligible, safe,
				want.eligible, want.sessionSafe)
		}
	}
}

func TestSkewedStatementDoesNotPoisonLaterEntries(t *testing.T) {
	// a statement the gate refuses leaves no key behind, and the next clean statement is judged on its own
	s := gateTestSession(t, nil)
	if outcome := s.gateQuery([]byte("WITH gone AS (DELETE FROM t RETURNING *) SELECT * FROM gone\x00")); outcome.key != "" {
		t.Fatalf("a refused statement must carry no cache key: %+v", outcome)
	}
	clean := s.gateQuery([]byte(gateDeltaQuery + "\x00"))
	other := gateTestSession(t, nil).gateQuery([]byte(gateDeltaQuery + "\x00"))
	if !clean.eligible || clean.key == "" || clean.key != other.key {
		t.Fatalf("expected the same key as an untouched session: %q vs %q", clean.key, other.key)
	}
}
