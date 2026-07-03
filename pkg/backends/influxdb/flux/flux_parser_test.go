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

	_, err = parseStep("|> aggregateWindow(fn: mean)")
	if err != ErrTimeRangeParsingFailed {
		t.Fatalf("parseStep error = %v", err)
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
}

func TestParseQueryErrors(t *testing.T) {
	t.Parallel()

	_, _, _, err := ParseQuery(`from("bucket")
|> aggregateWindow(every: not-a-duration, fn: mean)`)
	if err == nil {
		t.Fatal("expected parse step error")
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
