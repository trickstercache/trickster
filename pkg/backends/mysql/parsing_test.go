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

package mysql

import (
	"strconv"
	"testing"
)

const tq00 = `SELECT MIN(col1), AVG(col2) FROM table WHERE col1 >= 1589904000 AND col1 < 1589997600 GROUP BY col1 div 60 ORDER BY col1`
const tq01 = `SELECT MIN(col1), AVG(col2) FROM table WHERE col1 BETWEEN 1589904000 AND 1589997600 GROUP BY col1 div 60 ORDER BY col1`
const tq02 = `SELECT MIN(col1) as ts, SUM(col2) FROM table WHERE ts > 1589904000 AND ts <= 1589997600 GROUP BY ts div 60 ORDER BY col1`
const tq03 = `SELECT DATETIME(MIN(col1)) as ts, SUM(col2), COUNT() as ct FROM table WHERE ts BETWEEN 1589904000 AND 1589997600 GROUP BY ts div 30 ORDER BY ts`
const tq04 = `SELECT MIN(col1) as ts, AVG(col2) FROM table WHERE ts > 1589904000 AND ts <= 1589997600 GROUP BY ts div 60 ORDER BY col1 LIMIT 10`
const tq05 = `SELECT MIN as ts, AVG(col2) FROM table WHERE ts > 1589904000 AND ts <= 1589997600 GROUP BY ts ORDER BY ts`

func TestParseRawQuery(t *testing.T) {
	tests := []struct {
		query string
		err   error
	}{
		{tq00, nil},
		{tq01, nil},
		{tq02, nil},
		{tq03, nil},
		{tq04, ErrLimitUnsupported},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			trq, _, _, err := parse(test.query)
			if err != test.err {
				t.Errorf("got '%v' expected '%v'", err, test.err)
			}
			t.Logf("%+v\n", trq)
		})
	}
}
