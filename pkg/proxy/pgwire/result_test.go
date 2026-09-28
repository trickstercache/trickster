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

func testResult(rows ...string) *Result {
	r := &Result{RowDescription: []byte(resultTestDescription), times: []int64{}}
	for _, row := range rows {
		minute, label, _ := strings.Cut(row, ":")
		n := int(minute[0]-'0')*10 + int(minute[1]-'0')
		body, _ := (&pgproto3.DataRow{Values: [][]byte{[]byte(label)}}).Encode(nil)
		r.appendRow(body[frameHeaderLen:], resultTestBucket(n), true)
	}
	return r
}

func labels(t *testing.T, r *Result, descending bool) string {
	t.Helper()
	var out []string
	stream := r.encode(descending)
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

func TestResultMergeCropAndOrder(t *testing.T) {
	cached := testResult("00:a", "00:b", "05:c", "10:old")
	fetched := testResult("10:new1", "10:new2", "15:d")
	earlier := testResult("55:never") // sorts last: minute 55
	merged, err := mergeResults([]*Result{cached, fetched, testResult(), earlier})
	if err != nil {
		t.Fatal(err)
	}
	// the later part replaces bucket 10 whole; rows inside a bucket keep their order
	if got := labels(t, merged, false); got != "a b c new1 new2 d never SELECT 7" {
		t.Fatalf("merged: %q", got)
	}
	if got := labels(t, merged, true); got != "never d new1 new2 c a b SELECT 7" {
		t.Fatalf("descending: %q", got)
	}
	extent := timeseries.Extent{Start: time.Unix(0, resultTestBucket(5)), End: time.Unix(0, resultTestBucket(10))}
	if got := labels(t, merged.crop(extent), false); got != "c new1 new2 SELECT 3" {
		t.Fatalf("cropped: %q", got)
	}
	empty := merged.crop(timeseries.Extent{Start: time.Unix(0, resultTestBucket(20)), End: time.Unix(0, resultTestBucket(30))})
	if got := labels(t, empty, false); got != "SELECT 0" || empty.times == nil || empty.RowDescription == nil {
		t.Fatalf("an empty crop keeps the row description and its delta nature: %q", got)
	}
	kept, first, trimmed := merged.retain(2)
	if !trimmed || first != resultTestBucket(15) || labels(t, kept, false) != "d never SELECT 2" {
		t.Fatalf("retain: %q from %d (%t)", labels(t, kept, false), first, trimmed)
	}
	for _, limit := range []int{0, 5, 99} {
		if _, _, trimmed := merged.retain(limit); trimmed {
			t.Fatalf("a limit of %d must keep everything", limit)
		}
	}
	if _, err = mergeResults([]*Result{cached, nil}); !errors.Is(err, errResultRow) {
		t.Fatalf("expected a nil part to be rejected, got %v", err)
	}
	object := &Result{}
	object.appendRow([]byte{0, 0}, 0, false)
	if _, err = mergeResults([]*Result{cached, object}); !errors.Is(err, errResultRow) {
		t.Fatalf("expected an untimed part to be rejected, got %v", err)
	}
}

func TestResultSortIsStableWithinABucket(t *testing.T) {
	r := testResult("10:x", "10:y", "00:p", "05:m", "00:q")
	r.sortByTime()
	if got := labels(t, r, false); got != "p q m x y SELECT 5" {
		t.Fatalf("sorted: %q", got)
	}
}

func TestResultEncodeKeepsAnObjectsOwnTag(t *testing.T) {
	object := &Result{RowDescription: []byte(resultTestDescription), Tag: "SELECT 1"}
	object.appendRow([]byte{0, 1, 0, 0, 0, 1, 'v'}, 0, false)
	if got := labels(t, object, true); got != "v SELECT 1" {
		t.Fatalf("object: %q", got)
	}
	if got := labels(t, &Result{}, false); got != "SELECT 0" {
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
	object.appendRow([]byte("row-one"), 0, false)
	object.appendRow([]byte("row-two!"), 0, false)
	for name, original := range map[string]*Result{
		"delta": testResult("00:a", "00:b", "05:c"), "empty delta": testResult(), "object": object,
		"bare": {},
	} {
		encoded, err := codec.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := codec.Unmarshal(encoded)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(decoded.encode(false), original.encode(false)) || (decoded.times == nil) != (original.times == nil) ||
			codec.Size(decoded) != codec.Size(original) {
			t.Fatalf("%s: the round trip changed the result", name)
		}
	}
	if _, err := codec.Marshal(nil); !errors.Is(err, errResultCodec) || codec.Size(nil) != 0 {
		t.Fatalf("a nil result cannot be stored: %v", err)
	}
	valid, _ := codec.Marshal(testResult("00:a", "05:b"))
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
}

func FuzzResultCodec(f *testing.F) {
	valid, _ := resultCodec{}.Marshal(testResult("00:a", "05:b"))
	f.Add(valid)
	f.Add([]byte{resultCodecVersion, resultFlagTimes, 0, 0, 0xff, 0xff, 0xff, 0xff, 0x0f})
	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := resultCodec{}.Unmarshal(data)
		if err != nil {
			return
		}
		// whatever decodes must be safe to read end to end
		_ = decoded.encode(true)
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
	if !sqlanalyzer.AlignedToBucket(weekly, 7*24*time.Hour, phase) {
		t.Fatal("fixture: a Monday must lie on a Monday-phased weekly grid")
	}
	if _, err = bucketTime(row([]byte("2026-09-07 00:00:00+00")), 0, decoder, 7*24*time.Hour, phase); err != nil {
		t.Fatalf("a phased weekly bucket must be on the grid: %v", err)
	}
}

func TestFinalizeDelta(t *testing.T) {
	step := 5 * time.Minute
	plan := &sqlanalyzer.QueryPlan{Step: step, UpperBound: &sqlanalyzer.Bound{}}
	merged := testResult("00:a", "05:b", "10:c", "15:d")
	all := timeseries.ExtentList{{Start: time.Unix(0, resultTestBucket(0)), End: time.Unix(0, resultTestBucket(15))}}
	requested := timeseries.Extent{Start: time.Unix(0, resultTestBucket(5)), End: time.Unix(0, resultTestBucket(15))}
	longAfter := time.Unix(0, resultTestBucket(15)).Add(24 * time.Hour)

	response, retained, extents, err := finalizeDelta(&Config{RetentionPoints: 2}, plan, merged, all, requested, longAfter)
	if err != nil || labels(t, response, false) != "b c d SELECT 3" {
		t.Fatalf("retention must never trim the response: %q, %v", labels(t, response, false), err)
	}
	if labels(t, retained, false) != "c d SELECT 2" || len(extents) != 1 || !extents[0].Start.Equal(time.Unix(0, resultTestBucket(10))) {
		t.Fatalf("retained %q over %v", labels(t, retained, false), extents)
	}

	// ten minutes after the last bucket, a fifteen-minute tolerance leaves only the first stable
	soonAfter := time.Unix(0, resultTestBucket(15)).Add(10 * time.Minute)
	_, retained, extents, err = finalizeDelta(&Config{BackfillWindow: 15 * time.Minute}, plan, merged, all, requested, soonAfter)
	if err != nil || labels(t, retained, false) != "a b SELECT 2" || len(extents) != 1 {
		t.Fatalf("rows newer than the stable coverage must not be kept: %q over %v, %v", labels(t, retained, false), extents, err)
	}
	_, retained, extents, err = finalizeDelta(&Config{BackfillWindow: 48 * time.Hour}, plan, merged, all, requested, soonAfter)
	if err != nil || retained.Rows() != 0 || len(extents) != 0 {
		t.Fatalf("an entirely volatile result keeps nothing: %d rows over %v, %v", retained.Rows(), extents, err)
	}

	// an open-ended range is never stable in its final, still-filling bucket
	openEnded := &sqlanalyzer.QueryPlan{Step: step}
	atTheEdge := time.Unix(0, resultTestBucket(15)).Add(time.Minute)
	_, retained, _, err = finalizeDelta(&Config{}, openEnded, merged, all, requested, atTheEdge)
	// one step back from now truncates to 08:10, so that bucket is volatile as well
	if err != nil || labels(t, retained, false) != "a b SELECT 2" {
		t.Fatalf("open-ended: %q, %v", labels(t, retained, false), err)
	}
	if _, _, _, err = finalizeDelta(&Config{}, plan, &Result{}, all, requested, longAfter); !errors.Is(err, errResultRow) {
		t.Fatalf("an untimed result cannot be finalized, got %v", err)
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
