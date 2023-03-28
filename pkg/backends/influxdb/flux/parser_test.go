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
`
var testAbsoluteTime string = `from("test-bucket")
	|> range(start: 2023-01-01T00:00:00Z, stop: 2023-01-08T00:00:00Z)
`
var testUnixTime string = `from("test-bucket"
	|> range(start: 1672531200, stop: 1673136000)
`

var testsOK map[string]string = map[string]string{
	"RelativeDuration": testRelativeDuration,
	"AbsoluteTime":     testAbsoluteTime,
	"UnixTime":         testUnixTime,
}

func TestParserOK(t *testing.T) {
	for test, script := range testsOK {
		t.Run(test, func(t *testing.T) {
			p := NewParser(strings.NewReader(script))
			_, err := p.ParseQuery()
			if err != nil {
				t.Errorf("failed to parse valid script: %s", err)
			}
		})
	}
}

func TestRelativeDuration(t *testing.T) {
	p := NewParser(strings.NewReader(testRelativeDuration))
	now := time.Now()
	q, err := p.ParseQuery()
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
}

func TestRFC3999Time(t *testing.T) {
	p := NewParser(strings.NewReader(testAbsoluteTime))
	q, err := p.ParseQuery()
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
}

func TestUnixTime(t *testing.T) {
	p := NewParser(strings.NewReader(testUnixTime))
	q, err := p.ParseQuery()
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
}
