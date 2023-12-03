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
	"bufio"
	"io"
	"strconv"
	"strings"

	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
	"github.com/trickstercache/trickster/v2/pkg/util/timeconv"
)

type Parser struct {
	reader io.Reader
}

func NewParser(reader io.Reader) *Parser {
	return &Parser{
		reader: reader,
	}
}

// Parse a Flux query.
// Returns the query and an error, plus a bool indicating if the query can use the OPC or not.
// A 'true' value should be taken as the error being for Trickster (no timestep), but not necessarily for InfluxDB.
func (p *Parser) ParseQuery() (*Query, bool, error) {
	r := bufio.NewReader(p.reader)
	q := &Query{
		stmts: make([]Statement, 0),
	}
	var hasRange, hasWindow bool
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimSpace(line) + "\n"
		var stmt Statement
		if err != nil {
			if err == io.EOF {
				break
			} else {
				return nil, false, err
			}
		}
		if !hasRange && strings.Contains(line, "range") {
			stmt, err = parseRangeFilter(line, 0)
			if err != nil {
				return nil, false, err
			}
			q.Extent = stmt.(*RangeStatement).ext
			q.stmts = append(q.stmts, stmt)
			hasRange = true
		} else if !hasWindow && strings.Contains(strings.ToLower(line), "window") {
			q.Step, err = parseWindowFunction(line, 0)
			if err != nil {
				return nil, false, err
			}
			q.stmts = append(q.stmts, &ConstStatement{line})
			hasWindow = true
		} else {
			q.stmts = append(q.stmts, &ConstStatement{line})
		}
	}
	if !hasRange {
		return nil, false, ErrFluxSemantics("flux queries must have a range() filter")
	} else if !hasWindow {
		return nil, false, ErrFluxSemantics("flux queries in Trickster must have a window() filter to determine timestep")
	}
	return q, false, nil
}

// Parse a line that is a range filter range(start: $[start], stop: $[stop])
func parseRangeFilter(query string, at int) (Statement, error) {
	var start, stop time.Time
	var err error
	for i := at; i < len(query); {
		// If start: token at this index,
		if token := tstrings.Substring(query, i, len("start:")); token == "start:" {
			// find the start and end of the time argument
			timeArgStart := i + len("start:")
			if query[timeArgStart] == ' ' {
				timeArgStart++
			}
			timeArgEnd := timeArgStart + strings.IndexAny(query[timeArgStart:], " ,)")
			if timeArgEnd == -1 {
				return nil, ErrFluxSyntax(query[timeArgStart:timeArgStart+10]+"...", "couldn't parse time field from start argument")
			}
			// and try to parse that argument as a time field
			start, err = tryParseTimeField(query[timeArgStart:timeArgEnd])
			if err != nil {
				return nil, err
			}
			i = timeArgEnd
			continue
		}
		if token := tstrings.Substring(query, i, len("stop:")); token == "stop:" {
			// find the start and end of the time argument
			timeArgStart := i + len("stop:")
			if query[timeArgStart] == ' ' {
				timeArgStart++
			}
			timeArgEnd := timeArgStart + strings.IndexAny(query[timeArgStart:], " )")
			if timeArgEnd == -1 {
				return nil, ErrFluxSyntax(query[timeArgStart:timeArgStart+10]+"...", "couldn't parse time field from stop argument")
			}
			// and try to parse that argument as a time field
			stop, err = tryParseTimeField(query[timeArgStart:timeArgEnd])
			if err != nil {
				return nil, err
			}
			i = timeArgEnd
			continue
		}
		// Break loop when we hit a ')'
		if query[i] == ')' {
			break
		}
		i++
	}
	if start.IsZero() {
		return nil, ErrFluxSemantics("range() expressions require a valid start argument")
	}
	if stop.IsZero() {
		stop = time.Now()
	}
	return &RangeStatement{timeseries.Extent{Start: start, End: stop}}, nil
}

func parseWindowFunction(query string, at int) (time.Duration, error) {
	for i := at; i < len(query); i++ {
		if token := tstrings.Substring(query, i, len("every:")); token == "every:" {
			stepArgStart := i + len(token)
			if query[stepArgStart] == ' ' {
				stepArgStart++
			}
			stepArgEnd := stepArgStart + strings.IndexAny(query[stepArgStart:], ", )")
			if stepArgEnd == -1 {
				return 0, ErrFluxSyntax(query[stepArgStart:stepArgStart+10]+"...", "couldn't parse timestep from window function")
			}
			return timeconv.ParseDuration(query[stepArgStart:stepArgEnd])
		}
		if query[i] == ')' {
			break
		}
	}
	return 0, ErrFluxSyntax("window()", "couldn't find a timestep, make sure argument 'every:' is included")
}

func tryParseTimeField(s string) (time.Time, error) {
	var t time.Time
	var erd, eat, eut error
	if t, erd = tryParseRelativeDuration(s); erd == nil {
		return t, nil
	}
	if t, eat = tryParseAbsoluteTime(s); eat == nil {
		return t, nil
	}
	if t, eut = tryParseUnixTimestamp(s); eut == nil {
		return t, nil
	}
	return time.Time{}, ErrInvalidTimeFormat(erd, eat, eut)
}

func tryParseRelativeDuration(s string) (time.Time, error) {
	d, err := timeconv.ParseDuration(s)
	if err != nil {
		return time.Time{}, err
	}
	return time.Now().Add(d), nil
}

func tryParseAbsoluteTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

func tryParseUnixTimestamp(s string) (time.Time, error) {
	unix, err := strconv.Atoi(s)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(int64(unix), 0).UTC(), nil
}
