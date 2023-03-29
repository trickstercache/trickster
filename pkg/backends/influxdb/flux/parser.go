package flux

import (
	"bufio"
	"errors"
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
	q := &Query{}
	if raw, err := io.ReadAll(r); err != nil {
		return nil, false, err
	} else {
		content := string(raw)
		ridx := strings.Index(content, "|> range(")
		sidx := strings.Index(strings.ToLower(content), "window(")
		if ridx == -1 {
			return nil, false, ErrFluxSyntax("range()", "flux timerange query scripts must contain a range() function")
		}
		q.Extent, err = parseRangeFilter(content, ridx+len("|> range("))
		if err != nil {
			return nil, false, err
		}
		if sidx == -1 {
			return q, true, errors.New("trickster requires window() or aggregateWindow() to determine time step")
		}
		q.Step, err = parseWindowFunction(content, sidx+len("|"))
		if err != nil {
			return nil, false, err
		}
		q.Statement = content
	}
	return q, false, nil
}

// Parse a line that is a range filter range(start: $[start], stop: $[stop])
func parseRangeFilter(query string, at int) (timeseries.Extent, error) {
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
				return timeseries.Extent{}, ErrFluxSyntax(query[timeArgStart:timeArgStart+10]+"...", "couldn't parse time field from start argument")
			}
			// and try to parse that argument as a time field
			start, err = tryParseTimeField(query[timeArgStart:timeArgEnd])
			if err != nil {
				return timeseries.Extent{}, err
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
				return timeseries.Extent{}, ErrFluxSyntax(query[timeArgStart:timeArgStart+10]+"...", "couldn't parse time field from stop argument")
			}
			// and try to parse that argument as a time field
			stop, err = tryParseTimeField(query[timeArgStart:timeArgEnd])
			if err != nil {
				return timeseries.Extent{}, err
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
		return timeseries.Extent{}, ErrFluxSemantics("range() expressions require a valid start argument")
	}
	return timeseries.Extent{Start: start, End: stop}, nil
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
