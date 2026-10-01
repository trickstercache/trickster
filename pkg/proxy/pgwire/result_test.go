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
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/jackc/pgx/v5/pgproto3"
)

const resultTestDescription = "description"

func resultTestBucket(minute int) int64 {
	return time.Date(2026, 9, 10, 8, minute, 0, 0, time.UTC).UnixNano()
}

func labels(t *testing.T, stream []byte) string {
	t.Helper()
	// each DataRow's first column, then the CommandComplete tag
	var out []string
	for len(stream) > 0 {
		typ, body, err := readFrame(bytes.NewReader(stream), pgMaxMessageBody)
		if err != nil {
			t.Fatal(err)
		}
		stream = stream[frameHeaderLen+len(body):]
		switch typ {
		case msgDataRow:
			text, err := rowColumn(body, 0)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, string(text))
		case msgCommandComplete:
			out = append(out, string(trimNUL(body)))
		}
	}
	return strings.Join(out, " ")
}

// the whole response writeTo writes
func (r *Result) encode() []byte {
	var out bytes.Buffer
	w := frameWriter{w: &out, buffer: make([]byte, 0, pumpBufferSizeBytes)}
	r.writeTo(&w)
	w.flush()
	return out.Bytes()
}

func TestResultEncodeKeepsAnObjectsOwnTag(t *testing.T) {
	object := &Result{RowDescription: []byte(resultTestDescription), Tag: "SELECT 1"}
	object.appendRow([]byte{0, 1, 0, 0, 0, 1, 'v'})
	if got := labels(t, object.encode()); got != "v SELECT 1" {
		t.Fatalf("object: %q", got)
	}
	if got := labels(t, (&Result{}).encode()); got != "SELECT 0" {
		t.Fatalf("a result with no tag still completes: %q", got)
	}
}

func TestRowColumn(t *testing.T) {
	body, _ := (&pgproto3.DataRow{Values: [][]byte{[]byte("a"), nil, []byte("ccc")}}).Encode(nil)
	body = body[frameHeaderLen:]
	for index, want := range []string{"a", "", "ccc"} {
		got, err := rowColumn(body, index)
		if err != nil || string(got) != want || (index == 1) != (got == nil) {
			t.Fatalf("column %d: %q %v", index, got, err)
		}
	}
	for name, bad := range map[string][]byte{
		"no count": {0}, "index out of range": body[:2], "truncated size": body[:4],
		"size beyond body": {0, 1, 0, 0, 0, 9, 'x'}, "negative size": {0, 1, 0xff, 0xff, 0xff, 0xfe},
	} {
		index := 0
		if name == "index out of range" {
			index = 3
		}
		if _, err := rowColumn(bad, index); !errors.Is(err, errResultRow) {
			t.Fatalf("%s: expected errResultRow, got %v", name, err)
		}
	}
}

func TestResultCodecRoundTrip(t *testing.T) {
	codec := resultCodec{}
	object := &Result{RowDescription: []byte(resultTestDescription), Tag: "SELECT 2"}
	object.appendRow([]byte("row-one"))
	object.appendRow([]byte("row-two!"))
	for name, original := range map[string]*Result{"object": object, "bare": {}} {
		encoded, err := codec.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := codec.Unmarshal(encoded)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(decoded.encode(), original.encode()) || codec.Size(decoded) != codec.Size(original) {
			t.Fatalf("%s: the round trip changed the result", name)
		}
		// the decoded result refers to the encoding, and an append to its data can't reach past it
		if cap(decoded.data) != len(decoded.data) || cap(decoded.RowDescription) != len(decoded.RowDescription) {
			t.Fatalf("%s: the decoded slices reach past their values", name)
		}
		appended, err := codec.AppendMarshal([]byte("envelope"), original)
		if err != nil || string(appended[:8]) != "envelope" || !bytes.Equal(appended[8:], encoded) {
			t.Fatalf("%s: appended = %x, %v", name, appended, err)
		}
	}
	if _, err := codec.Marshal(nil); !errors.Is(err, errResultCodec) || codec.Size(nil) != 0 {
		t.Fatalf("a nil result cannot be stored: %v", err)
	}
	valid, _ := codec.Marshal(object)
	for cut := range len(valid) {
		if _, err := codec.Unmarshal(valid[:cut]); !errors.Is(err, errResultCodec) {
			t.Fatalf("a %d-byte prefix must be rejected, got %v", cut, err)
		}
	}
	wrongVersion := bytes.Clone(valid)
	wrongVersion[0]++
	if _, err := codec.Unmarshal(wrongVersion); !errors.Is(err, errResultCodec) {
		t.Fatalf("an unknown version must be rejected, got %v", err)
	}
	if _, err := codec.Unmarshal(append(bytes.Clone(valid), 'x')); !errors.Is(err, errResultCodec) {
		t.Fatalf("trailing bytes must be rejected, got %v", err)
	}
	// an entry written for delta rows, which carried bucket times, is a miss
	timed := bytes.Clone(valid)
	timed[1] = resultFlagTimes
	if _, err := codec.Unmarshal(timed); !errors.Is(err, errResultCodec) {
		t.Fatalf("a timed entry must be rejected, got %v", err)
	}
}

func FuzzResultCodec(f *testing.F) {
	object := &Result{RowDescription: []byte(resultTestDescription)}
	object.appendRow([]byte("row"))
	valid, _ := resultCodec{}.Marshal(object)
	f.Add(valid)
	f.Add([]byte{resultCodecVersion, resultFlagTimes, 0, 0, 0xff, 0xff, 0xff, 0xff, 0x0f})
	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := resultCodec{}.Unmarshal(data)
		if err != nil {
			return
		}
		// whatever decodes must be safe to read end to end
		_ = decoded.encode()
		for i := range decoded.ends {
			_ = decoded.row(i)
		}
	})
}

func timeAxisSettings(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestTimeAxisDecoding(t *testing.T) {
	iso := timeAxisSettings(map[string]string{
		"datestyle": "ISO, MDY", varTimeZone: "Etc/UTC", varExtraFloatDigits: fakeDefaultFloatDigits,
	})
	want := time.Date(2026, 9, 10, 8, 5, 0, 0, time.UTC)
	for name, test := range map[string]struct {
		kind TimeAxisKind
		unit timeseries.FieldDataType
		text string
		want time.Time
	}{
		"utc offset":          {TimeAxisTimestampTZ, 0, "2026-09-10 08:05:00+00", want},
		"negative offset":     {TimeAxisTimestampTZ, 0, "2026-09-10 04:05:00-04", want},
		"half-hour offset":    {TimeAxisTimestampTZ, 0, "2026-09-10 13:35:00+05:30", want},
		"local mean time":     {TimeAxisTimestampTZ, 0, "2026-09-10 08:14:21+00:09:21", want},
		"fractional seconds":  {TimeAxisTimestampTZ, 0, "2026-09-10 08:05:00.25+00", want.Add(250 * time.Millisecond)},
		"zone-less timestamp": {TimeAxisTimestamp, 0, "2026-09-10 08:05:00", want},
		"date":                {TimeAxisDate, 0, "2026-09-10", want.Truncate(24 * time.Hour)},
		"epoch seconds":       {TimeAxisEpochInteger, timeseries.DateTimeUnixSecs, "1789027500", want},
		"epoch millis":        {TimeAxisEpochInteger, timeseries.DateTimeUnixMilli, "1789027500000", want},
		"float epoch":         {TimeAxisEpochFloat, timeseries.DateTimeUnixSecs, "1789027500", want},
		"numeric epoch":       {TimeAxisEpochNumeric, timeseries.DateTimeUnixSecs, "1789027500.000", want},
	} {
		decoder, err := newTimeAxisDecoder(test.kind, test.unit, TimeSemantics{}, iso)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, err := decoder.decode([]byte(test.text)); err != nil || !got.Equal(test.want) {
			t.Fatalf("%s: got %v, %v; want %v", name, got, err, test.want)
		}
	}
	zoned, _ := newTimeAxisDecoder(TimeAxisTimestampTZ, 0, TimeSemantics{}, iso)
	for _, text := range []string{
		"", "infinity", "-infinity", "12026-09-10 08:05:00+00", "2026-09-10 08:05:00+00 BC", "2026-09-10 08:05:00",
		"2026-09-10 08:05:00Z", "2026-09-10 08:05:00.+00", "2026-09-10 08:05:00.1234567890+00", "2026-13-10 08:05:00+00",
		"2026-09-10 08:05:00+0", "2026-09-10 08:05:00+00:00:00:00", "2026-09-10 08:05:00+00:7", "2026-09-10 08:05:00+00:99",
		"10.09.2026 08:05:00 UTC",
	} {
		if _, err := zoned.decode([]byte(text)); !errors.Is(err, errTimeAxis) {
			t.Fatalf("%q must fail closed, got %v", text, err)
		}
	}
	naive, _ := newTimeAxisDecoder(TimeAxisTimestamp, 0, TimeSemantics{}, iso)
	if _, err := naive.decode([]byte("2026-09-10 08:05:00+00")); !errors.Is(err, errTimeAxis) {
		t.Fatalf("a zone-less column cannot carry an offset, got %v", err)
	}
	date, _ := newTimeAxisDecoder(TimeAxisDate, 0, TimeSemantics{}, iso)
	if _, err := date.decode([]byte("2026-09-10 BC")); !errors.Is(err, errTimeAxis) {
		t.Fatalf("expected a BC date to fail closed, got %v", err)
	}
	epoch, _ := newTimeAxisDecoder(TimeAxisEpochFloat, timeseries.DateTimeUnixSecs, TimeSemantics{}, iso)
	for _, text := range []string{"2e+09x", "1789027500.5", "1e300"} {
		if _, err := epoch.decode([]byte(text)); !errors.Is(err, errTimeAxis) {
			t.Fatalf("%q must fail closed, got %v", text, err)
		}
	}
	integer, _ := newTimeAxisDecoder(TimeAxisEpochInteger, timeseries.DateTimeUnixSecs, TimeSemantics{}, iso)
	for _, text := range []string{"abc", "9223372036854775807"} {
		if _, err := integer.decode([]byte(text)); !errors.Is(err, errTimeAxis) {
			t.Fatalf("%q must fail closed, got %v", text, err)
		}
	}
}

func TestISOTimestampsParseAsTimeParseDoes(t *testing.T) {
	// the parser never accepts what time.Parse refuses or reads it differently, and takes every
	// canonical value; it refuses forms PostgreSQL never sends, like a padded hour
	const layout = "2006-01-02 15:04:05"
	var inputs []string
	for _, base := range []string{
		"2026-09-10 08:05:00", "2024-02-29 23:59:59", "2023-02-29 00:00:00", "2026-04-31 12:00:00",
		"0000-01-01 00:00:00", "2026-12-31 24:00:00", "2026-01-01 00:60:00", "2026-01-01 00:00:60",
	} {
		inputs = append(inputs, base)
		for i := range base {
			for _, c := range "0139 -:a+" {
				mutated := []byte(base)
				mutated[i] = byte(c)
				inputs = append(inputs, string(mutated))
			}
		}
	}
	for _, text := range inputs {
		want, wantErr := time.Parse(layout, text)
		got, err := parseISOTimestamp([]byte(text), false)
		canonical := wantErr == nil && want.Format(layout) == text
		if (err == nil && (wantErr != nil || !got.Equal(want))) || (err != nil && canonical) {
			t.Fatalf("%q: got %v, %v; time.Parse gives %v, %v", text, got, err, want, wantErr)
		}
	}
}

func TestTimeAxisDecoderFailsClosedOnSessionSettings(t *testing.T) {
	for name, test := range map[string]struct {
		kind     TimeAxisKind
		unit     timeseries.FieldDataType
		naiveUTC bool
		settings map[string]string
		ok       bool
	}{
		"unknown DateStyle":                 {TimeAxisTimestampTZ, 0, false, nil, false},
		"German DateStyle":                  {TimeAxisTimestampTZ, 0, false, map[string]string{"datestyle": "German, DMY"}, false},
		"zone-less under a local zone":      {TimeAxisTimestamp, 0, false, map[string]string{"datestyle": "ISO", varTimeZone: gateZoneNewYork}, false},
		"zone-less, zone unknown":           {TimeAxisDate, 0, false, map[string]string{"datestyle": "ISO"}, false},
		"zone-less but the engine says UTC": {TimeAxisTimestamp, 0, true, map[string]string{"datestyle": "iso, mdy", varTimeZone: gateZoneNewYork}, true},
		"epoch without a unit":              {TimeAxisEpochInteger, timeseries.DateTimeRFC3339Nano, false, nil, false},
		"float with lossy digits":           {TimeAxisEpochFloat, timeseries.DateTimeUnixSecs, false, map[string]string{varExtraFloatDigits: "-3"}, false},
		"float with unreadable digits":      {TimeAxisEpochFloat, timeseries.DateTimeUnixSecs, false, map[string]string{varExtraFloatDigits: "x"}, false},
		// the origin never announces the setting, and a role default of -14 renders 1788998400 as 2e+09
		"float with unknown digits":     {TimeAxisEpochFloat, timeseries.DateTimeUnixMicro, false, nil, false},
		"float with zero digits":        {TimeAxisEpochFloat, timeseries.DateTimeUnixMicro, false, map[string]string{varExtraFloatDigits: "0"}, true},
		"numeric needs no float digits": {TimeAxisEpochNumeric, timeseries.DateTimeUnixSecs, false, nil, true},
		"float with exact digits":       {TimeAxisEpochFloat, timeseries.DateTimeUnixNano, false, map[string]string{varExtraFloatDigits: "3"}, true},
		"unknown kind":                  {0, 0, false, nil, false},
	} {
		_, err := newTimeAxisDecoder(test.kind, test.unit, TimeSemantics{NaiveTimestampsAreUTC: test.naiveUTC}, timeAxisSettings(test.settings))
		if (err == nil) != test.ok {
			t.Errorf("%s: err = %v, want ok = %t", name, err, test.ok)
		}
	}
}

func TestBucketTime(t *testing.T) {
	decoder, _ := newTimeAxisDecoder(TimeAxisTimestampTZ, 0, TimeSemantics{}, timeAxisSettings(map[string]string{"datestyle": "ISO"}))
	row := func(values ...[]byte) []byte {
		body, _ := (&pgproto3.DataRow{Values: values}).Encode(nil)
		return body[frameHeaderLen:]
	}
	got, err := bucketTime(row([]byte("x"), []byte("2026-09-10 08:05:00+00")), 1, decoder, 5*time.Minute, 0)
	if err != nil || got != resultTestBucket(5) {
		t.Fatalf("got %d, %v", got, err)
	}
	for name, body := range map[string][]byte{
		"null bucket": row([]byte("x"), nil), "off the grid": row([]byte("x"), []byte("2026-09-10 08:07:00+00")),
		"unreadable": row([]byte("x"), []byte("yesterday")), "missing column": row([]byte("x")),
	} {
		if _, err := bucketTime(body, 1, decoder, 5*time.Minute, 0); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	// a weekly bucket is phased from the Unix epoch, which began on a Thursday
	weekly := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	phase := 4 * 24 * time.Hour
	if !timeseries.OnGrid(weekly, 7*24*time.Hour, phase) {
		t.Fatal("fixture: a Monday must lie on a Monday-phased weekly grid")
	}
	if _, err = bucketTime(row([]byte("2026-09-07 00:00:00+00")), 0, decoder, 7*24*time.Hour, phase); err != nil {
		t.Fatalf("a phased weekly bucket must be on the grid: %v", err)
	}
}

func TestOrderable(t *testing.T) {
	plan := &sqlanalyzer.QueryPlan{OutputColumn: "time"}
	if !orderable(plan) || descending(plan) {
		t.Fatal("no ORDER BY leaves the engine free to emit buckets in time order")
	}
	plan.Ordering = []sqlanalyzer.OrderTerm{{Column: "time", Descending: true}, {Column: "host"}}
	if !orderable(plan) || !descending(plan) {
		t.Fatal("the bucket as the leading term can be honored in either direction")
	}
	plan.Ordering = []sqlanalyzer.OrderTerm{{Column: "host"}, {Column: "time"}}
	if orderable(plan) {
		t.Fatal("a group column ahead of the bucket cannot be rebuilt from merged buckets")
	}
}

func TestUpstreamHandoff(t *testing.T) {
	var handoff upstreamHandoff
	handoff.init()
	if handoff.requested() {
		t.Fatal("a new handoff is not requested")
	}
	handoff.finish()
	handoff.mtx.Lock()
	finished := handoff.finished
	handoff.mtx.Unlock()
	if !finished {
		t.Fatal("expected the handoff to be finished")
	}
}

func (r *Result) appendRow(body []byte) {
	r.data = append(r.data, body...)
	r.ends = append(r.ends, uint32(len(r.data))) // #nosec G115 -- test rows are small
}
