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

package sqlguard

import "testing"

func TestScan(t *testing.T) {
	words := map[string]WordClass{
		"random": Volatile, "now": Clock, "current_timestamp": BareClock,
		"only": Unfaithful, "gapfill": Gapfill, "locf": Carry, "unnest": SetReturning,
	}
	for sql, want := range map[string]Facts{
		"SELECT RANDOM /* between */ ()":                               {Volatile: true},
		`SELECT "random"()`:                                            {Volatile: true},
		"SELECT schema.random()":                                       {Volatile: true},
		"SELECT random, 'random()', $$now()$$ /* current_timestamp */": {},
		`SELECT "RANDOM"(), "only", "current_timestamp"`:               {},
		"SELECT now()":                                                 {Clock: true}, "SELECT current_timestamp": {Clock: true},
		"SELECT * FROM ONLY t": {Unfaithful: true}, "SELECT U&'a'": {Unfaithful: true},
		`SELECT U&"a"`:                       {Unfaithful: true},
		"SELECT gapfill(), locf(), unnest()": {Gapfill: true, Carries: true, SetReturning: true},
	} {
		t.Run(sql, func(t *testing.T) {
			if got := Scan(sql, words); got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
	if got := Scan("SELECT random()", nil); got != (Facts{}) {
		t.Fatal("word table was not dialect-specific")
	}
}
