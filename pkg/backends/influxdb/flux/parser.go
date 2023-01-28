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
	valid := false
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
				return nil, ErrFluxSyntax(line, "flux scripts must begin with from(bucket: ...)")
			}
		}
		// Check for range
		if strings.Contains(line, "range") {
			q.Extent, err = parseRangeFilter(line)
			if err != nil {
				return nil, err
			}
			valid = true
		}
		q.Statement += line + " "
		ln++
	}
	if !valid {
		return nil, ErrFluxSemantics("script is not valid flux")
	}
	return q, nil
}

// Parse a line that is a range filter range(start: $[start], stop: $[stop])
func parseRangeFilter(line string) (timeseries.Extent, error) {
	tokens := strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '(' || r == ')' || r == ','
	})
	var start, stop time.Time
	var err error
	for i, token := range tokens {
		if token == "start:" {
			start, err = tryParseTimeField(tokens[i+1])
			if err != nil {
				return timeseries.Extent{}, err
			}
		}
		if token == "stop:" {
			stop, err = tryParseTimeField(tokens[i+1])
			if err != nil {
				return timeseries.Extent{}, err
			}
		}
	}
	if start.IsZero() {
		return timeseries.Extent{}, ErrFluxSemantics("range() expressions require a valid start argument")
	}
	return timeseries.Extent{Start: start, End: stop}, nil
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
	d, err := duration.ParseDuration(s)
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
