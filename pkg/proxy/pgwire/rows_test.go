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
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	rowsTestTime  = "time"
	rowsTestHost  = "host"
	rowsTestValue = "value"
	rowsTestOIDs  = 25 // text
)

func rowsTestSession(t *testing.T) *session {
	t.Helper()
	server, err := NewServer(Config{
		BackendName: t.Name(), Dialect: testProvider, Upstream: Upstream{Address: testUnusedAddress},
		Analyzer: testAnalyzer, Engine: testEngine{}, MaxQuerySizeBytes: 1 << 20, MaxMessageSizeBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &session{front: server, server: server, user: testClientUser, database: testDatabase}
	s.tracker = newSessionTracker(s.user, s.database, nil)
	// the settings an origin announces at login, which timestamptz text depends on
	s.tracker.reported["datestyle"], s.tracker.reported[varTimeZone] = "ISO, MDY", "UTC"
	return s
}

func rowsTestPlan(ordering ...sqlanalyzer.OrderTerm) *sqlanalyzer.QueryPlan {
	return &sqlanalyzer.QueryPlan{
		OutputColumn: rowsTestTime, GroupColumns: []string{rowsTestHost}, Step: 5 * time.Minute,
		Ordering: ordering,
	}
}

func rowsTestDescription(fields ...pgproto3.FieldDescription) []byte {
	if fields == nil {
		fields = []pgproto3.FieldDescription{
			{Name: []byte(rowsTestTime), DataTypeOID: OIDTimestampTZ},
			{Name: []byte(rowsTestHost), DataTypeOID: rowsTestOIDs},
			{Name: []byte(rowsTestValue), DataTypeOID: OIDInt8},
		}
	}
	body, _ := (&pgproto3.RowDescription{Fields: fields}).Encode(nil)
	return body[frameHeaderLen:]
}

func rowsTestRow(minute int, host []byte, value string) []byte {
	at := time.Date(2026, 9, 10, 8, minute, 0, 0, time.UTC).Format("2006-01-02 15:04:05") + "+00"
	body, _ := (&pgproto3.DataRow{Values: [][]byte{[]byte(at), host, []byte(value)}}).Encode(nil)
	return body[frameHeaderLen:]
}

func sinkRows(t *testing.T, plan *sqlanalyzer.QueryPlan, rows ...[]byte) *nativedelta.Delta {
	t.Helper()
	sink := newRowSink(plan)
	if err := sink.describe(rowsTestSession(t), rowsTestDescription()); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 0, 256)
	for _, row := range rows {
		// the fetch hands every row over in one reused buffer
		buffer = append(buffer[:0], row...)
		if err := sink.row(buffer); err != nil {
			t.Fatal(err)
		}
	}
	d, err := sink.finish()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// the whole response writeDelta writes
func encodeDelta(d *nativedelta.Delta, plan *sqlanalyzer.QueryPlan) []byte {
	var out bytes.Buffer
	w := frameWriter{w: &out, buffer: make([]byte, 0, pumpBufferSizeBytes)}
	writeDelta(&w, d, plan)
	w.flush()
	return out.Bytes()
}

func valuesOf(t *testing.T, stream []byte) string {
	t.Helper()
	// each DataRow's host and value, then the CommandComplete tag
	var out []string
	for len(stream) > 0 {
		typ, body, err := readFrame(bytes.NewReader(stream), pgMaxMessageBody)
		if err != nil {
			t.Fatal(err)
		}
		stream = stream[frameHeaderLen+len(body):]
		switch typ {
		case msgDataRow:
			host, _ := rowColumn(body, 1)
			value, _ := rowColumn(body, 2)
			if host == nil {
				host = []byte("NULL")
			}
			out = append(out, string(host)+"="+string(value))
		case msgCommandComplete:
			out = append(out, string(trimNUL(body)))
		}
	}
	return strings.Join(out, " ")
}

func TestRowSink(t *testing.T) {
	rows := [][]byte{
		rowsTestRow(10, []byte("b"), "3"), rowsTestRow(5, []byte("a"), "1"),
		rowsTestRow(5, nil, "2"), rowsTestRow(5, []byte{}, "4"), rowsTestRow(10, []byte("a"), "5"),
	}
	d := sinkRows(t, rowsTestPlan(), rows...)
	if !bytes.Equal(d.Header, rowsTestDescription()) || d.Rows() != len(rows) {
		t.Fatalf("header %q, %d rows", d.Header, d.Rows())
	}
	// a series per host, a NULL host apart from an empty one, and every row byte-exact
	series := d.DS.Results[0].SeriesList
	if len(series) != 4 {
		t.Fatalf("got %d series", len(series))
	}
	seen := map[string]bool{}
	for _, s := range series {
		for _, p := range dspoints.Of(s) {
			body, _ := p.Values[0].([]byte)
			seen[string(body)] = true
		}
	}
	for _, row := range rows {
		if !seen[string(row)] {
			t.Fatalf("row %q was not kept as sent", row)
		}
	}
	// the response orders buckets by time, and a bucket's rows by series
	if got := valuesOf(t, encodeDelta(d, rowsTestPlan())); got != "a=1 NULL=2 =4 b=3 a=5 SELECT 5" {
		t.Fatalf("ascending: %q", got)
	}
	descendingPlan := rowsTestPlan(sqlanalyzer.OrderTerm{Column: rowsTestTime, Descending: true})
	if got := valuesOf(t, encodeDelta(d, descendingPlan)); got != "b=3 a=5 a=1 NULL=2 =4 SELECT 5" {
		t.Fatalf("descending: %q", got)
	}
}

func TestRowSinkKeepsTheOriginsOrderWithinABucket(t *testing.T) {
	// ordering by more than the bucket is the origin's to decide within each bucket, and it can
	// differ from bucket to bucket, so the rows keep the order they arrived in
	plan := rowsTestPlan(sqlanalyzer.OrderTerm{Column: rowsTestTime}, sqlanalyzer.OrderTerm{Column: rowsTestValue})
	d := sinkRows(t, plan, rowsTestRow(5, []byte("b"), "1"), rowsTestRow(5, []byte("a"), "2"),
		rowsTestRow(10, []byte("a"), "1"), rowsTestRow(10, []byte("b"), "2"))
	const want = "b=1 a=2 a=1 b=2 SELECT 4"
	if got := valuesOf(t, encodeDelta(d, plan)); got != want {
		t.Fatalf("got %q", got)
	}
	// the order survives the cache encoding, which reads each sequence back as an int64
	stored, err := dataset.MarshalDataSet(d.DS, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := dataset.UnmarshalDataSet(stored, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &nativedelta.Delta{Header: d.Header, DS: ts.(*dataset.DataSet)}
	if got := valuesOf(t, encodeDelta(decoded, plan)); got != want {
		t.Fatalf("after the cache: %q", got)
	}
}

func TestRowSinkFailures(t *testing.T) {
	s := rowsTestSession(t)
	field := func(name string, oid uint32, format int16) pgproto3.FieldDescription {
		return pgproto3.FieldDescription{Name: []byte(name), DataTypeOID: oid, Format: format}
	}
	for name, description := range map[string][]byte{
		"not a description": {0xff},
		"no time column":    rowsTestDescription(field(rowsTestHost, rowsTestOIDs, 0)),
		"two time columns": rowsTestDescription(field(rowsTestTime, OIDTimestampTZ, 0),
			field(rowsTestTime, OIDTimestampTZ, 0), field(rowsTestHost, rowsTestOIDs, 0)),
		"binary time":    rowsTestDescription(field(rowsTestTime, OIDTimestampTZ, 1), field(rowsTestHost, rowsTestOIDs, 0)),
		"no time axis":   rowsTestDescription(field(rowsTestTime, rowsTestOIDs, 0), field(rowsTestHost, rowsTestOIDs, 0)),
		"no group":       rowsTestDescription(field(rowsTestTime, OIDTimestampTZ, 0)),
		"two groups":     rowsTestDescription(field(rowsTestTime, OIDTimestampTZ, 0), field(rowsTestHost, rowsTestOIDs, 0), field(rowsTestHost, rowsTestOIDs, 0)),
		"numeric bucket": rowsTestDescription(field(rowsTestTime, OIDNumeric, 0), field(rowsTestHost, rowsTestOIDs, 0)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := newRowSink(rowsTestPlan()).describe(s, description); !errors.Is(err, nativedelta.ErrUnmergeable) {
				t.Fatalf("expected an unmergeable result, got %v", err)
			}
		})
	}
	sink := newRowSink(rowsTestPlan())
	if err := sink.row(rowsTestRow(5, []byte("a"), "1")); !errors.Is(err, errResultRow) {
		t.Fatalf("a row before the description: %v", err)
	}
	if _, err := sink.finish(); !errors.Is(err, errResultRow) {
		t.Fatalf("a result with no description: %v", err)
	}
	if err := sink.describe(s, rowsTestDescription()); err != nil {
		t.Fatal(err)
	}
	offGrid := time.Date(2026, 9, 10, 8, 7, 0, 0, time.UTC).Format("2006-01-02 15:04:05") + "+00"
	body, _ := (&pgproto3.DataRow{Values: [][]byte{[]byte(offGrid), []byte("a"), []byte("1")}}).Encode(nil)
	if err := sink.row(body[frameHeaderLen:]); !errors.Is(err, errTimeAxis) {
		t.Fatalf("a bucket off the plan's grid: %v", err)
	}
	if err := sink.row([]byte{0, 3, 0, 0, 0, 1}); !errors.Is(err, errResultRow) {
		t.Fatalf("a truncated row: %v", err)
	}
	truncated := rowsTestRow(5, []byte("abc"), "1")
	if err := sink.row(truncated[:len(truncated)-6]); !errors.Is(err, errResultRow) {
		t.Fatalf("a row cut inside its group column: %v", err)
	}
	// one bucket can hold only one row for each group: a repeat is caught as it arrives, or, when
	// the rows come out of time order, once the result is complete
	if err := sink.row(rowsTestRow(5, []byte("a"), "1")); err != nil {
		t.Fatal(err)
	}
	if err := sink.row(rowsTestRow(5, []byte("a"), "2")); !errors.Is(err, errResultRow) {
		t.Fatalf("a repeated group: %v", err)
	}
	unordered := newRowSink(rowsTestPlan())
	if err := unordered.describe(s, rowsTestDescription()); err != nil {
		t.Fatal(err)
	}
	for _, minute := range []int{10, 5, 10} {
		if err := unordered.row(rowsTestRow(minute, []byte("a"), "1")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := unordered.finish(); !errors.Is(err, errResultRow) {
		t.Fatalf("a repeated group out of order: %v", err)
	}
}

func TestCommandCompleteFlushesWhenFull(t *testing.T) {
	var out bytes.Buffer
	// room for the frame's header, but not its tag
	w := frameWriter{w: &out, buffer: make([]byte, 0, 2*frameHeaderLen+4)}
	w.frame(msgDataRow, []byte("abcd"))
	writeSelectComplete(&w, 12345)
	w.flush()
	if got := valuesOf(t, out.Bytes()); !strings.HasSuffix(got, "SELECT 12345") {
		t.Fatalf("got %q", got)
	}
}
