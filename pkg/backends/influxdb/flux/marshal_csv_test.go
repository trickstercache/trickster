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

package flux

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func assertFluxCSVTables(t *testing.T, b []byte, wantRows, wantTables int) {
	t.Helper()
	reader := csv.NewReader(bytes.NewReader(b))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var rows, tables int
	var columns int
	for _, record := range records {
		if record[0] == "#datatype" {
			tables++
			columns = len(record)
		} else if record[0] == "" && len(record) > 1 && record[1] != "result" {
			rows++
		}
		if len(record) != columns {
			t.Fatalf("table %d has %d columns, row has %d: %v", tables, columns, len(record), record)
		}
	}
	if rows != wantRows || tables != wantTables {
		t.Fatalf("parsed %d rows in %d tables, want %d rows in %d tables", rows, tables, wantRows, wantTables)
	}
}

func TestMarshalTimeseriesCSVWriter(t *testing.T) {
	_, err := MarshalTimeseries(nil, nil, 200)
	if err != timeseries.ErrUnknownFormat {
		t.Error("expected ErrUnknownFormat got", err)
	}
	b, err := MarshalTimeseries(testDataSet(), &timeseries.RequestOptions{}, 200)
	if err != nil {
		t.Error(err)
	}
	if len(b) == 0 {
		t.Error("expected non-nil response body")
	}
	if string(b) != testDataSetAsCSV {
		t.Error("unexpected CSV response\n" + string(b))
	}
	ts, err := UnmarshalTimeseries([]byte(testDataSetAsCSV), testTRQ)
	if err != nil {
		t.Error(err)
	}
	b, err = MarshalTimeseries(ts, &timeseries.RequestOptions{}, 200)
	if err != nil {
		t.Error(err)
	}
	if len(b) == 0 {
		t.Error("expected non-nil response body")
	}
	if string(b) != testDataSetAsCSV {
		t.Error("unexpected CSV response\n" + string(b))
	}
}

func TestMarshalTimeseriesCSVRepeatsTableHeader(t *testing.T) {
	ds := testDataSet()
	ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, ds.Results[0].SeriesList[0])
	b, err := MarshalTimeseries(ds, &timeseries.RequestOptions{}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "#datatype"); got != 2 {
		t.Fatalf("marshaled %d table headers, want 2: %s", got, b)
	}
	assertFluxCSVTables(t, b, 6, 2)
}
