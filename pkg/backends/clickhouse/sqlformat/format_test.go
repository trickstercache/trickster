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

package sqlformat

import "testing"

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
		sql, format, sel, err := Split(test.sql, "JSON")
		if err != nil || sql != test.want || format != test.format || sel != test.selectQuery {
			t.Fatalf("%s: %q %q %v %v", test.sql, sql, format, sel, err)
		}
	}
	for _, sql := range []string{"SELECT 1; SELECT 2", "USE other", "SET max_threads=2", ""} {
		if _, _, _, err := Split(sql, "JSON"); err == nil {
			t.Fatalf("accepted %q", sql)
		}
	}
}
