package flux

import (
	"bufio"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/util/timeconv/duration"
)

type Parser struct {
	reader io.Reader
}

func NewParser(reader io.Reader) *Parser {
	return &Parser{
		reader: reader,
	}
}

func (p *Parser) ParseQuery() (*Query, error) {
	r := bufio.NewReader(p.reader)
	ln := 0
	q := &Query{}
	for line, err := r.ReadString('\n'); ; line, err = r.ReadString('\n') {
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			} else {
				return nil, err
			}
		}
		// Check for line 0 "from"
		if ln == 0 {
			if !strings.Contains(line, "from") {
				return nil, FluxSyntax(line, "flux scripts must begin with from()")
			}
		}
		// Check for range
		if strings.Contains(line, "range") {
			q.Extent, err = parseRangeFilter(line)
			if err != nil {
				return nil, err
			}
		}
		q.Statement += line + " "
		ln++
	}
	return q, nil
}

// Parse a line that is a range filter range(start: $[start], stop: $[stop])
func parseRangeFilter(line string) (timeseries.Extent, error) {
	tokens := strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '(' || r == ')' || r == ','
	})
	var start, stop time.Time
	var ok bool
	for i, token := range tokens {
		if token == "start:" {
			start, ok = tryParseTimeField(tokens[i+1])
			if !ok {
				return timeseries.Extent{}, FluxSyntax(token, "must be a valid duration literal, RFC3339 time, or UNIX time")
			}
		}
		if token == "stop:" {
			stop, ok = tryParseTimeField(tokens[i+1])
			if !ok {
				return timeseries.Extent{}, FluxSyntax(token, "must be a valid duration literal, RFC3339 time, or UNIX time")
			}
		}
	}
	if start.IsZero() {
		return timeseries.Extent{}, FluxSemantics("range() expressions require a valid start argument")
	}
	return timeseries.Extent{Start: start, End: stop}, nil
}

func tryParseTimeField(s string) (t time.Time, ok bool) {
	if t, ok = tryParseRelativeDuration(s); ok {
		return
	}
	if t, ok = tryParseAbsoluteTime(s); ok {
		return
	}
	if t, ok = tryParseUnixTimestamp(s); ok {
		return
	}
	return
}

func tryParseRelativeDuration(s string) (t time.Time, ok bool) {
	d, err := duration.ParseDuration(s)
	if err != nil {
		return time.Time{}, false
	}
	return time.Now().Add(d), true
}

func tryParseAbsoluteTime(s string) (t time.Time, ok bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func tryParseUnixTimestamp(s string) (t time.Time, ok bool) {
	unix, err := strconv.Atoi(s)
	if err != nil {
		return t, false
	}
	return time.Unix(int64(unix), 0).UTC(), true
}
