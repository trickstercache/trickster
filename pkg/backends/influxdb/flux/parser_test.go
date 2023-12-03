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
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/timeconv"
)

var testRelativeDuration string = `from("test-bucket")
	|> range(start: -7d, stop: -6d)
	|> window(every: 1m)
	|> mean()
	|> window(every: 10s)
`
var testAbsoluteTime string = `from("test-bucket")
	|> range(start: 2023-01-01T00:00:00Z, stop: 2023-01-08T00:00:00Z)
	|> window(every: 5m)
	|> mean()
	|> window(every: 10s)
`
var testUnixTime string = `from("test-bucket")
	|> range(start: 1672531200, stop: 1673136000)
	|> aggregateWindow(every: 30s, fn: mean)
`

var testNoRange string = `from("test-bucket")
	|> aggregateWindow(every: 30s, fn: mean)
`
var testNoStart string = `from("test-bucket")
	|> range(stop: 10)
	|> aggregateWindow(every: 30s, fn: mean)
`
var testNoWindow string = `from("test-bucket")
	|> range(start: 0, stop: 10)
`

var testsOK map[string]string = map[string]string{
	"RelativeDuration": testRelativeDuration,
	"AbsoluteTime":     testAbsoluteTime,
	"UnixTime":         testUnixTime,
}

var testsNotOK map[string]string = map[string]string{
	"FailNoRange":  testNoRange,
	"FailNoStart":  testNoStart,
	"FailNoWindow": testNoWindow,
}

func TestParserOK(t *testing.T) {
	for test, script := range testsOK {
		t.Run(test, func(t *testing.T) {
			p := NewParser(strings.NewReader(script))
			_, _, err := p.ParseQuery()
			if err != nil {
				t.Errorf("failed to parse valid script: %s", err)
			}
		})
	}
	for test, script := range testsNotOK {
		t.Run(test, func(t *testing.T) {
			p := NewParser(strings.NewReader(script))
			_, _, err := p.ParseQuery()
			if err == nil {
				t.Errorf("parsed invalid script")
			}
		})
	}
}

func TestRelativeDuration(t *testing.T) {
	p := NewParser(strings.NewReader(testRelativeDuration))
	now := time.Now()
	q, _, err := p.ParseQuery()
	if err != nil {
		t.Errorf("failed to parse valid script: %s", err)
		t.FailNow()
	}
	start := now.Add(-7 * timeconv.Day).Truncate(time.Second)
	stop := now.Add(-6 * timeconv.Day).Truncate(time.Second)
	qStartApprox := q.Extent.Start.Truncate(time.Second)
	qStopApprox := q.Extent.End.Truncate(time.Second)
	if !start.Equal(qStartApprox) {
		t.Errorf("query start time incorrect; got %v, should be %v", qStartApprox, start)
	}
	if !stop.Equal(qStopApprox) {
		t.Errorf("query stop time incorrect; got %v, should be %v", qStopApprox, stop)
	}
	if q.Step != timeconv.Minute {
		t.Errorf("query step incorrect; got %v, should be %v", q.Step, timeconv.Minute)
	}
}

func TestRFC3999Time(t *testing.T) {
	p := NewParser(strings.NewReader(testAbsoluteTime))
	q, _, err := p.ParseQuery()
	if err != nil {
		t.Errorf("failed to parse valid script: %s", err)
		t.FailNow()
	}
	start := time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC)
	stop := time.Date(2023, time.January, 8, 0, 0, 0, 0, time.UTC)
	if !start.Equal(q.Extent.Start) {
		t.Errorf("query start time incorrect; got %v, should be %v", q.Extent.Start, start)
	}
	if !stop.Equal(q.Extent.End) {
		t.Errorf("query stop time incorrect; got %v, should be %v", q.Extent.End, stop)
	}
	if q.Step != 5*timeconv.Minute {
		t.Errorf("query step incorrect; got %v, should be %v", q.Step, 5*timeconv.Minute)
	}
}

func TestUnixTime(t *testing.T) {
	p := NewParser(strings.NewReader(testUnixTime))
	q, _, err := p.ParseQuery()
	if err != nil {
		t.Errorf("failed to parse valid script: %s", err)
		t.FailNow()
	}
	start := time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC)
	stop := time.Date(2023, time.January, 8, 0, 0, 0, 0, time.UTC)
	if !start.Equal(q.Extent.Start) {
		t.Errorf("query start time incorrect; got %v, should be %v", q.Extent.Start, start)
	}
	if !stop.Equal(q.Extent.End) {
		t.Errorf("query stop time incorrect; got %v, should be %v", q.Extent.End, stop)
	}
	if q.Step != 30*timeconv.Second {
		t.Errorf("query step incorrect; got %v, should be %v", q.Step, 30*timeconv.Second)
	}
}
