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
	"errors"
	"testing"
	"time"
)

func TestDefaultJSONRequestBody(t *testing.T) {
	t.Parallel()

	rb := DefaultJSONRequestBody()
	if rb.Type != LangFlux || rb.Dialect.Delimiter != "," {
		t.Fatalf("unexpected defaults: %+v", rb)
	}
	if len(DefaultAnnotations()) != 3 {
		t.Fatalf("annotations = %v", DefaultAnnotations())
	}
}

func TestVndfluxToJSON(t *testing.T) {
	t.Parallel()

	rb := vndfluxToJSON([]byte("from(\"bucket\")"))
	if rb.Query != `from("bucket")` || rb.Type != LangFlux {
		t.Fatalf("unexpected body: %+v", rb)
	}
}

func TestParseStep(t *testing.T) {
	t.Parallel()

	d, err := parseStep(`|> aggregateWindow(every: 1m, fn: mean)`)
	if err != nil || d != time.Minute {
		t.Fatalf("parseStep = (%v, %v)", d, err)
	}

	d, err = parseStep(`|> window(every: 5m)`)
	if err != nil || d != 5*time.Minute {
		t.Fatalf("parseStep trailing paren = (%v, %v)", d, err)
	}

	_, err = parseStep("|> aggregateWindow(fn: mean)")
	if err != ErrTimeRangeParsingFailed {
		t.Fatalf("parseStep error = %v", err)
	}

	_, err = parseStep("|> window(every: 1m")
	if err != ErrTimeRangeParsingFailed {
		t.Fatalf("parseStep missing closer = %v", err)
	}
}

func TestParseAggregateWindow(t *testing.T) {
	t.Parallel()

	const prefix = "|> aggregateWindow(every: 1h, fn: mean"
	tests := []struct {
		name        string
		line        string
		expected    windowSpec
		expectedErr error
	}{
		{"default stop-time labels", prefix + ")", windowSpec{step: time.Hour}, nil},
		{
			"explicit stop-time labels", prefix + `, timeSrc: "_stop")`,
			windowSpec{step: time.Hour},
			nil,
		},
		{
			"start-time labels", prefix + `, timeSrc: "_start")`,
			windowSpec{step: time.Hour, labelsAtStart: true},
			nil,
		},
		{
			"other time source is refused", prefix + `, timeSrc: "_time")`,
			windowSpec{step: time.Hour},
			ErrUnsupportedWindow,
		},
		{
			"offset sets the phase", prefix + ", offset: 15m)",
			windowSpec{step: time.Hour, phase: 15 * time.Minute},
			nil,
		},
		{
			"negative offset wraps into the step", prefix + ", offset: -15m)",
			windowSpec{step: time.Hour, phase: 45 * time.Minute},
			nil,
		},
		{
			"offset beyond the step wraps", prefix + ", offset: 75m)",
			windowSpec{step: time.Hour, phase: 15 * time.Minute},
			nil,
		},
		{
			"period equal to every is allowed", prefix + ", period: 1h)",
			windowSpec{step: time.Hour},
			nil,
		},
		{
			"overlapping period is refused", prefix + ", period: 2h)",
			windowSpec{step: time.Hour},
			ErrUnsupportedWindow,
		},
		{
			"unterminated period is refused", "|> aggregateWindow(every: 1h, period: 1h",
			windowSpec{step: time.Hour},
			ErrUnsupportedWindow,
		},
		{
			"location is refused",
			prefix + `, location: timezone.location(name: "America/Chicago"))`,
			windowSpec{step: time.Hour},
			ErrUnsupportedWindow,
		},
		{
			"zero step is refused", "|> aggregateWindow(every: 0s, fn: mean)",
			windowSpec{},
			ErrUnsupportedWindow,
		},
		{
			"missing every is refused", "|> aggregateWindow(fn: mean)",
			windowSpec{},
			ErrTimeRangeParsingFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			w, err := parseAggregateWindow(test.line)
			if !errors.Is(err, test.expectedErr) {
				t.Fatalf("expected error %v got %v", test.expectedErr, err)
			}
			if err == nil && w != test.expected {
				t.Errorf("expected %+v got %+v", test.expected, w)
			}
		})
	}

	if _, err := parseAggregateWindow(prefix + ", offset: soon)"); err == nil {
		t.Error("expected an invalid offset to fail")
	}
}

func TestParseQueryRefusesWindow(t *testing.T) {
	t.Parallel()

	_, _, _, err := ParseQuery(`from(bucket: "b") |> range(start: -1h, stop: now()) ` +
		`|> window(every: 1m) |> mean()`)
	if !errors.Is(err, ErrUnsupportedWindow) {
		t.Fatalf("expected %v got %v", ErrUnsupportedWindow, err)
	}
}

func TestParseRange(t *testing.T) {
	t.Parallel()

	e, err := parseRange(`|> range(start: 1672531200, stop: 1673136000)`)
	if err != nil {
		t.Fatalf("parseRange: %v", err)
	}
	if e.Start.Unix() != 1672531200 || e.End.Unix() != 1673136000 {
		t.Fatalf("extent = %+v", e)
	}

	_, err = parseRange("|> range(start: bad, stop: 1)")
	if err == nil {
		t.Fatal("expected parse error")
	}

	_, err = parseRange("|> range(stop: 1)")
	if err != ErrTimeRangeParsingFailed {
		t.Fatalf("missing start = %v", err)
	}

	_, err = parseRange("|> range(start: 1)")
	if err != ErrTimeRangeParsingFailed {
		t.Fatalf("missing stop = %v", err)
	}

	_, err = parseRange("|> range(start: 1, stop: 2, stop: 3)")
	if err != ErrTimeRangeParsingFailed {
		t.Fatalf("too many parts = %v", err)
	}

	_, err = parseRange("|> range(start: 1, stop: not-a-time)")
	if err == nil {
		t.Fatal("expected end parse error")
	}
}

func TestTryParseTimeField(t *testing.T) {
	t.Parallel()

	tm, err := tryParseRelativeDuration("-7d")
	if err != nil || tm.IsZero() {
		t.Fatalf("relative duration = (%v, %v)", tm, err)
	}

	tm, err = tryParseAbsoluteTime("2023-01-01T00:00:00Z")
	if err != nil || tm.Unix() != 1672531200 {
		t.Fatalf("absolute time = (%v, %v)", tm, err)
	}

	tm, err = tryParseUnixTimestamp("1672531200")
	if err != nil || tm.Unix() != 1672531200 {
		t.Fatalf("unix timestamp = (%v, %v)", tm, err)
	}

	_, err = tryParseTimeField("not-a-time")
	if err != ErrTimeRangeParsingFailed {
		t.Fatalf("tryParseTimeField = %v", err)
	}
}

func TestTokenizeRangeLine(t *testing.T) {
	t.Parallel()

	line := `from("bucket") |> range(start: -7d, stop: -6d)`
	start := len(`from("bucket") `)
	out := tokenizeRangeLine(line, start)
	if out != `from("bucket") |> range(<TIMERANGE_TOKEN>)` {
		t.Fatalf("tokenizeRangeLine = %q", out)
	}

	unclosed := `from("bucket") |> range(start: -7d, stop: -6d`
	if got := tokenizeRangeLine(unclosed, start); got != unclosed {
		t.Fatalf("unclosed tokenizeRangeLine = %q", got)
	}
}

func TestParseQueryErrors(t *testing.T) {
	t.Parallel()

	_, _, _, err := ParseQuery(`from("bucket")
|> aggregateWindow(every: not-a-duration, fn: mean)`)
	if err == nil {
		t.Fatal("expected parse step error")
	}

	_, _, _, err = ParseQuery(`from("bucket")
|> range(start: bad, stop: also-bad)`)
	if err == nil {
		t.Fatal("expected parse range error")
	}

	_, e, _, err := ParseQuery(`from("bucket")
|> range(start: 1, stop: 2)`)
	if err != nil || e.Start.Unix() != 1 {
		t.Fatalf("ParseQuery unix range = (%+v, %v)", e, err)
	}
}

func TestParseQueryRelativeRange(t *testing.T) {
	t.Parallel()

	_, e, d, err := ParseQuery(testFluxQuery1)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if d != time.Minute {
		t.Fatalf("step = %v", d)
	}
	if e.End.Before(e.Start) {
		t.Fatalf("expected ordered extent, got %+v", e)
	}
}
