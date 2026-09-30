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

package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream/streamtest"
)

func TestSQLStreamConformance(t *testing.T) {
	base := envelope(`[[18446744073709551615,2000000000,"a"],[9007199254740993,1000000000,"\u0061"],[null,1000000000,null],[7,2000000000,"null"]]`, 4)
	var fields map[string]json.RawMessage
	raw, err := json.Marshal(decoded(t, []byte(base)).Output[0].Records)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	lateSchema := fmt.Sprintf(`{"execution_time_ms":0,"output":[{"records":{"rows":%s,"total_rows":4,"schema":%s,"metrics":{}}}]}`, fields["rows"], fields["schema"])
	for name, body := range map[string]string{
		"rows":         base,
		"schema_last":  lateSchema,
		"empty":        envelope(`[]`, 0),
		"metrics_null": strings.Replace(base, `"total_rows":4`, `"total_rows":4,"metrics":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newDecoder, streamtest.Case{
				TRQ: query(), Body: []byte(body),
				Unwrap: func(ts timeseries.Timeseries) *dataset.DataSet { return ts.(*dataSet).DataSet },
			})
			ts, err := UnmarshalTimeseriesReader(strings.NewReader(body), query())
			if err != nil {
				t.Fatal(err)
			}
			if name != "empty" && (ts.SeriesCount() != 3 || ts.ValueCount() != 4) {
				t.Fatalf("tag encoding changed identity: %d series, %d values", ts.SeriesCount(), ts.ValueCount())
			}
		})
	}
	for name, body := range map[string]string{
		"truncated":               base[:len(base)-1],
		"trailing":                base + `{}`,
		"duplicate_output":        strings.Replace(base, `"output":`, `"output":[],"output":`, 1),
		"duplicate_rows":          strings.Replace(base, `"rows":`, `"rows":[],"rows":`, 1),
		"duplicate_schema":        strings.Replace(base, `"total_rows":4`, `"total_rows":4,"schema":{}`, 1),
		"wrong_total":             strings.Replace(base, `"total_rows":4`, `"total_rows":5`, 1),
		"bad_tag_after_good_rows": envelope(`[[1,1000000000,"a"],[2,2000000000,42]]`, 2),
		"escaped_duplicate_point": envelope(`[[1,1000000000,"a"],[2,1000000000,"\u0061"]]`, 2),
		"schema_last_null_rows":   strings.Replace(lateSchema, string(fields["rows"]), "null", 1),
	} {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newDecoder, streamtest.Case{TRQ: query(), Body: []byte(body), WantErr: streamtest.ErrAny})
		})
	}
}
