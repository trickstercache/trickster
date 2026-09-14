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
	"bufio"
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/ClickHouse/ch-go/proto"
	"github.com/ClickHouse/clickhouse-go/v2/lib/column"
)

func TestTSVEscapeRoundTrip(t *testing.T) {
	for _, raw := range []string{"plain", "tab\there", "nl\nhere", "nul\x00\x00", "back\\slash", "it's", "\b\f\r", ""} {
		if got := unescapeTSV(escapeTSV(raw)); got != raw {
			t.Errorf("round trip %q -> %q", raw, got)
		}
	}
	if got := unescapeTSV(`Enum8(\'orange\' = 1, \'blue\' = 2)`); got != "Enum8('orange' = 1, 'blue' = 2)" {
		t.Errorf("enum type row: %q", got)
	}
	if got := unescapeTSV(nullToken); got != nullToken {
		t.Errorf("null literal changed: %q", got)
	}
	if got := unescapeTSV(`a\qb\`); got != `a\qb\` {
		t.Errorf("unknown escape changed: %q", got)
	}
	if got := escapeTSV("IUA\x00\x00"); got != `IUA\0\0` {
		t.Errorf("escape nul: %q", got)
	}
}

// the exact TSVWithNamesAndTypes shape ClickHouse returns for a bucketed
// query with enum, fixed-string, date and array columns
const escapedTSVFixture = "t\tcab_type\tp\td\tc\n" +
	"DateTime\tEnum8(\\'orange\\' = 1, \\'blue\\' = 2, \\'purple\\' = 3)\tFixedString(4)\tDate32\tUInt64\n" +
	"2026-09-14 03:15:00\tblue\tIUA\\0\t2026-09-14\t3\n" +
	"2026-09-14 03:15:00\torange\tOV\\0\\0\t2026-09-14\t15\n" +
	"2026-09-14 03:20:00\tpurple\tA\\'B\t1969-12-31\t7\n"

func fixtureTRQ() *timeseries.TimeRangeQuery {
	trq := testTRQ.Clone()
	trq.TimestampDefinition = timeseries.FieldDefinition{Name: "t", DataType: timeseries.DateTimeSQL}
	trq.TagFieldDefintions = timeseries.FieldDefinitions{{Name: "cab_type"}, {Name: "p"}}
	return trq
}

func TestTSVToNativeRoundTrip(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(escapedTSVFixture), fixtureTRQ())
	if err != nil {
		t.Fatal(err)
	}
	ds := ts.(*dataset.DataSet)
	fields, _, _, _ := ds.FieldDefinitions()
	for _, f := range fields {
		if f.Name == "cab_type" && f.SDataType != "Enum8('orange' = 1, 'blue' = 2, 'purple' = 3)" {
			t.Fatalf("enum type row not unescaped: %q", f.SDataType)
		}
	}
	var out bytes.Buffer
	if err := marshalTimeseriesNative(&out, ds, &timeseries.RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	// decode the block with the official client, exactly as a Grafana plugin would
	br := bufio.NewReader(bytes.NewReader(out.Bytes()))
	if peek, _ := br.Peek(1); peek[0] == 1 {
		if err := skipBlockInfo(br); err != nil {
			t.Fatal(err)
		}
	}
	numCols, _ := readUvarint(br)
	numRows, _ := readUvarint(br)
	consumed := out.Len() - br.Buffered()
	pr := proto.NewReader(bytes.NewReader(out.Bytes()[consumed:]))
	got := map[string][]any{}
	for range numCols {
		name, _ := pr.Str()
		typ, _ := pr.Str()
		if _, err := pr.ReadByte(); err != nil { // custom serialization flag
			t.Fatal(err)
		}
		col, err := column.Type(typ).Column(name, &column.ServerContext{Revision: server.ServerRevision, Timezone: time.UTC})
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if err := col.Decode(pr, int(numRows)); err != nil {
			t.Fatalf("%s (%s): %v", name, typ, err)
		}
		for i := range int(numRows) {
			got[name] = append(got[name], col.Row(i, false))
		}
	}
	cabs := make([]string, 0, 3)
	for _, v := range got["cab_type"] {
		cabs = append(cabs, v.(string))
	}
	if strings.Join(cabs, ",") != "blue,orange,purple" {
		t.Fatalf("enum values: %v", cabs)
	}
	if p, ok := got["p"][0].(string); !ok || p != "IUA\x00" {
		t.Fatalf("fixed string: %q", got["p"][0])
	}
	if p := got["p"][2].(string); p != "A'B\x00" {
		t.Fatalf("fixed string with quote: %q", p)
	}
	if d, ok := got["d"][2].(time.Time); !ok || d.Format("2006-01-02") != "1969-12-31" {
		t.Fatalf("date32 value: %v", got["d"][2])
	}
}

func TestTSVOutputEscapes(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(escapedTSVFixture), fixtureTRQ())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := marshalTimeseriesXSV(&out, ts.(*dataset.DataSet), nil, true, true, '\t'); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{`Enum8(\'orange\' = 1, \'blue\' = 2, \'purple\' = 3)`, "\tIUA\\0\t", "\tA\\'B\t"} {
		if !strings.Contains(text, want) {
			t.Errorf("TSV output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\"") {
		t.Errorf("TSV output must not be CSV-quoted:\n%s", text)
	}
}

func TestMarshalNative_EncodeErrorWritesNothing(t *testing.T) {
	fixture := "t\ta\tc\nDateTime\tFixedString(2)\tUInt64\n2026-09-14 03:15:00\tabcde\t3\n"
	trq := testTRQ.Clone()
	trq.TimestampDefinition = timeseries.FieldDefinition{Name: "t", DataType: timeseries.DateTimeSQL}
	trq.TagFieldDefintions = timeseries.FieldDefinitions{{Name: "a"}}
	ts, err := UnmarshalTimeseries([]byte(fixture), trq)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := marshalTimeseriesNative(&out, ts.(*dataset.DataSet), &timeseries.RequestOptions{}); err == nil {
		t.Fatal("expected an encode error for an oversized FixedString value")
	}
	if out.Len() != 0 {
		t.Fatalf("partial body written: %d bytes", out.Len())
	}
}

func TestMarshalRowsAreTimeOrderedAcrossSeries(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(escapedTSVFixture), fixtureTRQ())
	if err != nil {
		t.Fatal(err)
	}
	ds := ts.(*dataset.DataSet)
	if len(ds.Results[0].SeriesList) < 3 {
		t.Fatalf("expected one series per cab type, got %d", len(ds.Results[0].SeriesList))
	}
	var out bytes.Buffer
	if err := marshalTimeseriesXSV(&out, ds, nil, true, true, '\t'); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")[2:]
	var prev string
	for _, line := range lines {
		ts, _, _ := strings.Cut(line, "\t")
		if ts < prev {
			t.Fatalf("rows not time-ordered:\n%s", out.String())
		}
		prev = ts
	}
	if !strings.HasPrefix(lines[0], "2026-09-14 03:15:00\tblue") || !strings.HasPrefix(lines[1], "2026-09-14 03:15:00\torange") {
		t.Fatalf("series order within a bucket changed:\n%s", out.String())
	}
}
