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

package model

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/tinylib/msgp/msgp"
)

// MarshalTimeseries renders a Timeseries in the client's requested format
func MarshalTimeseries(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	w := new(bytes.Buffer)
	if err := MarshalTimeseriesWriter(ts, rlo, status, w); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// MarshalTimeseriesWriter renders a Timeseries to an io.Writer in the
// client's requested format, as graphite-web's renderView would
func MarshalTimeseriesWriter(ts timeseries.Timeseries, rlo *timeseries.RequestOptions,
	status int, w io.Writer,
) error {
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		return timeseries.ErrUnknownFormat
	}
	ro := renderOptions(rlo)
	series := flatten(ds)
	var contentType string
	var body []byte
	var err error
	switch ro.Format {
	case "", FormatJSON:
		contentType = headers.ValueApplicationJSON
		if ro.JSONP != "" {
			contentType = "text/javascript"
		}
		body, err = renderJSON(series, ro)
	case FormatRaw:
		contentType = "text/plain"
		body = renderRaw(series)
	case FormatCSV:
		contentType = "text/csv"
		body = renderCSV(series, ro)
	case FormatMsgPack:
		contentType = "application/x-msgpack"
		body, err = renderMsgPack(series, ro)
	default:
		return timeseries.ErrUnknownFormat
	}
	if err != nil {
		return err
	}
	if hw, ok := w.(http.ResponseWriter); ok && hw != nil {
		hw.Header().Set(headers.NameContentType, contentType)
		if status > 0 {
			hw.WriteHeader(status)
		}
	}
	_, err = w.Write(body)
	return err
}

// series is the graphite-web TimeSeries view of one DataSet series
type series struct {
	name   string
	tags   dataset.Tags
	start  int64 // epoch seconds of the first point
	end    int64 // epoch seconds one step past the last point
	step   int64
	values []*float64
	// consolidation state, JSON only
	valuesPerPoint int
}

// flatten turns the DataSet into graphite-web series, in DataSet order.
// Points are the buckets the origin returned (nulls included), so start,
// end and step are recovered from them; a series with fewer than two points
// takes the query's step.
func flatten(ds *dataset.DataSet) []*series {
	var step int64
	if ds.TimeRangeQuery != nil {
		step = int64(ds.TimeRangeQuery.Step / time.Second)
	}
	var out []*series
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			sr := &series{name: s.Header.Name, tags: s.Header.Tags, step: step, valuesPerPoint: 1}
			if ss := seriesStep(&s.Header); ss > 0 {
				sr.step = ss
			}
			if len(s.Points) > 0 {
				sr.start = int64(s.Points[0].Epoch) / int64(time.Second)
				if len(s.Points) > 1 {
					sr.step = (int64(s.Points[1].Epoch) - int64(s.Points[0].Epoch)) / int64(time.Second)
				}
				if sr.step <= 0 {
					sr.step = 1
				}
				sr.end = int64(s.Points[len(s.Points)-1].Epoch)/int64(time.Second) + sr.step
				sr.values = make([]*float64, len(s.Points))
				for i, p := range s.Points {
					if len(p.Values) > 0 {
						if f, ok := p.Values[0].(float64); ok {
							v := f
							sr.values[i] = &v
						}
					}
				}
			}
			out = append(out, sr)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// JSON (renderViewJson)

func renderJSON(all []*series, ro RenderOptions) ([]byte, error) {
	var buf strings.Builder
	buf.WriteByte('[')
	first := true
	if len(all) > 0 {
		// consolidation uses the time range spanned by every series
		startTime, endTime := all[0].start, all[0].end
		for _, s := range all[1:] {
			startTime, endTime = min(startTime, s.start), max(endTime, s.end)
		}
		timeRange := endTime - startTime
		for _, s := range all {
			if ro.MaxDataPoints > 0 {
				s.consolidateFor(ro.MaxDataPoints, timeRange, ro.XFilesFactor)
			}
			dps := s.datapoints(ro.XFilesFactor)
			if ro.NoNullPoints {
				dps = slices.DeleteFunc(dps, func(d datapoint) bool { return d.v == nil || math.IsNaN(*d.v) })
				if len(dps) == 0 {
					continue
				}
			}
			if !first {
				buf.WriteString(", ")
			}
			first = false
			buf.WriteString(`{"target": `)
			writePyJSONString(&buf, s.name)
			buf.WriteString(`, "tags": `)
			writeTags(&buf, s.tags)
			buf.WriteString(`, "datapoints": [`)
			for i, d := range dps {
				if i > 0 {
					buf.WriteString(", ")
				}
				buf.WriteByte('[')
				if d.v == nil || math.IsNaN(*d.v) {
					buf.WriteString("null")
				} else if math.IsInf(*d.v, 0) {
					// graphite-web emits Infinity and then rewrites it
					if *d.v > 0 {
						buf.WriteString("1e9999")
					} else {
						buf.WriteString("-Infinity")
					}
				} else {
					buf.WriteString(PyFloat(*d.v))
				}
				buf.WriteString(", ")
				buf.WriteString(strconv.FormatInt(d.ts, 10))
				buf.WriteByte(']')
			}
			buf.WriteString("]}")
		}
	}
	buf.WriteByte(']')
	out := []byte(buf.String())
	if ro.Pretty {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, out, "", "  "); err != nil {
			return nil, err
		}
		out = pretty.Bytes()
	}
	if ro.JSONP != "" {
		out = append(append(append([]byte(ro.JSONP), '('), out...), ')')
	}
	return out, nil
}

type datapoint struct {
	v  *float64
	ts int64
}

// consolidateFor reproduces renderViewJson's maxDataPoints handling,
// including the start "nudge" that aligns consolidation bands across
// refreshes and its off-by-one (it drops valuesToLose-1 values)
func (s *series) consolidateFor(maxDataPoints int, timeRange int64, _ float64) {
	if maxDataPoints == 1 {
		s.valuesPerPoint = max(len(s.values), 1)
		return
	}
	if s.step <= 0 {
		return
	}
	numberOfDataPoints := float64(timeRange) / float64(s.step)
	if float64(maxDataPoints) >= numberOfDataPoints {
		return
	}
	valuesPerPoint := int(math.Ceil(numberOfDataPoints / float64(maxDataPoints)))
	secondsPerPoint := int64(valuesPerPoint) * s.step
	nudge := secondsPerPoint + pyMod(s.start, s.step) - pyMod(s.start, secondsPerPoint)
	s.start += nudge
	valuesToLose := int(nudge / s.step)
	for r := 1; r < valuesToLose && len(s.values) > 0; r++ {
		s.values = s.values[1:]
	}
	s.valuesPerPoint = valuesPerPoint
}

// pyMod is Python's % for int64 (result has the divisor's sign)
func pyMod(a, b int64) int64 {
	m := a % b
	if m != 0 && (m < 0) != (b < 0) {
		m += b
	}
	return m
}

// datapoints reproduces TimeSeries.datapoints(): the (possibly
// consolidated) values zipped with timestamps from start to end inclusive
// at step*valuesPerPoint, truncated to the shorter
func (s *series) datapoints(xff float64) []datapoint {
	vals := s.values
	if s.valuesPerPoint > 1 {
		vals = consolidate(s.values, s.valuesPerPoint, consolidationFunc(s.tags), xff)
	}
	span := s.step * int64(s.valuesPerPoint)
	if span <= 0 {
		span = 1
	}
	n := 0
	if s.end >= s.start {
		n = int((s.end-s.start)/span) + 1
	}
	n = min(n, len(vals))
	out := make([]datapoint, n)
	for i := range out {
		out[i] = datapoint{v: vals[i], ts: s.start + int64(i)*span}
	}
	return out
}

// consolidationFunc is the series' consolidation function: the
// consolidateBy tag graphite-web sets, else average
func consolidationFunc(tags dataset.Tags) string {
	if cf, ok := tags["consolidateBy"]; ok && cf != "" {
		return cf
	}
	return DefaultConsolidationFunc
}

// consolidate reproduces TimeSeries.__consolidatingGenerator
func consolidate(values []*float64, perPoint int, cf string, xff float64) []*float64 {
	if cf == "avg" {
		cf = "average"
	}
	var out []*float64
	buf := make([]float64, 0, perPoint)
	valcnt, nonNull := 0, 0
	flush := func() {
		if nonNull > 0 && float64(nonNull)/float64(perPoint) >= xff {
			v := applyCF(cf, buf)
			out = append(out, &v)
		} else {
			out = append(out, nil)
		}
		buf, valcnt, nonNull = buf[:0], 0, 0
	}
	for _, x := range values {
		valcnt++
		if x != nil {
			buf = append(buf, *x)
			nonNull++
		} else if cf == "avg_zero" {
			buf = append(buf, 0)
		}
		if valcnt == perPoint {
			flush()
		}
	}
	if valcnt > 0 {
		flush()
	}
	return out
}

func applyCF(cf string, usable []float64) float64 {
	switch cf {
	case "sum":
		s := 0.0
		for _, v := range usable {
			s += v
		}
		return s
	case "max":
		return slices.Max(usable)
	case "min":
		return slices.Min(usable)
	case "first":
		return usable[0]
	case "last":
		return usable[len(usable)-1]
	}
	// average and avg_zero (whose buffer already holds zeros for nulls)
	s := 0.0
	for _, v := range usable {
		s += v
	}
	return s / float64(len(usable))
}

// writeTags renders the tags object: "name" first (graphite-web inserts it
// first), then the remaining keys sorted, which is the one place Trickster's
// output can differ from graphite-web's (Python preserves the order in which
// functions added their tags)
func writeTags(buf *strings.Builder, tags dataset.Tags) {
	buf.WriteByte('{')
	keys := make([]string, 0, len(tags))
	for k := range tags {
		if k != "name" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	if _, ok := tags["name"]; ok {
		keys = append([]string{"name"}, keys...)
	}
	for i, k := range keys {
		if i > 0 {
			buf.WriteString(", ")
		}
		writePyJSONString(buf, k)
		buf.WriteString(": ")
		writePyJSONString(buf, tags[k])
	}
	buf.WriteByte('}')
}

// writePyJSONString encodes a string as Python's json.dumps does with
// ensure_ascii=True: non-ASCII as \uXXXX (surrogate pairs above the BMP),
// control characters as \n \r \t \b \f or \u00XX, and no escaping of <, >
// or &
func writePyJSONString(buf *strings.Builder, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				buf.WriteString(`\"`)
			case '\\':
				buf.WriteString(`\\`)
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			case '\b':
				buf.WriteString(`\b`)
			case '\f':
				buf.WriteString(`\f`)
			default:
				if c < 0x20 || c == 0x7f {
					buf.WriteString(`\u00`)
					buf.WriteByte("0123456789abcdef"[c>>4])
					buf.WriteByte("0123456789abcdef"[c&0xf])
				} else {
					buf.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r > 0xffff {
			r -= 0x10000
			writeU(buf, 0xd800+(r>>10))
			writeU(buf, 0xdc00+(r&0x3ff))
		} else {
			writeU(buf, r)
		}
	}
	buf.WriteByte('"')
}

func writeU(buf *strings.Builder, r rune) {
	const hex = "0123456789abcdef"
	buf.WriteString(`\u`)
	buf.WriteByte(hex[(r>>12)&0xf])
	buf.WriteByte(hex[(r>>8)&0xf])
	buf.WriteByte(hex[(r>>4)&0xf])
	buf.WriteByte(hex[r&0xf])
}

// PyFloat formats a float the way Python's repr() does: the shortest
// round-tripping digits, in fixed notation for decimal exponents in
// [-4, 16) with a trailing ".0" for integral values, otherwise scientific
// with a signed two-digit-minimum exponent (1e+16, 3.3164e-06)
func PyFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // [-]d[.ddd]e±XX
	neg := e[0] == '-'
	if neg {
		e = e[1:]
	}
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	digits := strings.Replace(mant, ".", "", 1)
	var out string
	switch {
	case exp < -4 || exp >= 16:
		out = mant + "e" + expStr
	case exp < 0:
		out = "0." + strings.Repeat("0", -exp-1) + digits
	case len(digits) <= exp+1:
		out = digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
	default:
		out = digits[:exp+1] + "." + digits[exp+1:]
	}
	if neg {
		return "-" + out
	}
	return out
}

// ---------------------------------------------------------------------------
// raw (renderViewRaw): <name>,<start>,<end>,<step>|repr,repr,None\n

func renderRaw(all []*series) []byte {
	var buf strings.Builder
	for _, s := range all {
		buf.WriteString(s.name)
		buf.WriteByte(',')
		buf.WriteString(strconv.FormatInt(s.start, 10))
		buf.WriteByte(',')
		buf.WriteString(strconv.FormatInt(s.end, 10))
		buf.WriteByte(',')
		buf.WriteString(strconv.FormatInt(s.step, 10))
		buf.WriteByte('|')
		for i, v := range s.values {
			if i > 0 {
				buf.WriteByte(',')
			}
			if v == nil {
				buf.WriteString("None")
			} else {
				buf.WriteString(PyFloat(*v))
			}
		}
		buf.WriteByte('\n')
	}
	return []byte(buf.String())
}

// ---------------------------------------------------------------------------
// csv (renderViewCsv): name,YYYY-MM-DD HH:MM:SS,value\r\n in the request tz

func renderCSV(all []*series, ro RenderOptions) []byte {
	loc := ro.Location
	if loc == nil {
		loc = time.UTC
	}
	var buf strings.Builder
	for _, s := range all {
		for i, v := range s.values {
			csvField(&buf, s.name)
			buf.WriteByte(',')
			buf.WriteString(time.Unix(s.start+int64(i)*s.step, 0).In(loc).Format("2006-01-02 15:04:05"))
			buf.WriteByte(',')
			if v != nil {
				buf.WriteString(PyFloat(*v))
			}
			buf.WriteString("\r\n")
		}
	}
	return []byte(buf.String())
}

// csvField quotes a field as Python's csv excel dialect does
func csvField(buf *strings.Builder, s string) {
	if !strings.ContainsAny(s, ",\"\r\n") {
		buf.WriteString(s)
		return
	}
	buf.WriteByte('"')
	buf.WriteString(strings.ReplaceAll(s, `"`, `""`))
	buf.WriteByte('"')
}

// ---------------------------------------------------------------------------
// msgpack (renderViewMsgPack): [TimeSeries.getInfo(), ...]

func renderMsgPack(all []*series, ro RenderOptions) ([]byte, error) {
	var b []byte
	b = appendArrayHeader(b, len(all))
	for i, s := range all {
		b = msgp.AppendMapHeader(b, 9)
		b = msgp.AppendString(b, "name")
		b = msgp.AppendString(b, s.name)
		// Python's msgpack picks the smallest unsigned encoding for
		// non-negative ints (uint32 for epoch seconds)
		b = msgp.AppendString(b, "start")
		b = appendInt(b, s.start)
		b = msgp.AppendString(b, "end")
		b = appendInt(b, s.end)
		b = msgp.AppendString(b, "step")
		b = appendInt(b, s.step)
		b = msgp.AppendString(b, "values")
		b = appendArrayHeader(b, len(s.values))
		for _, v := range s.values {
			if v == nil {
				b = msgp.AppendNil(b)
			} else {
				b = msgp.AppendFloat64(b, *v)
			}
		}
		b = msgp.AppendString(b, "pathExpression")
		// graphite-web reports the target expression that produced a series,
		// which for a wildcard covers several series. One entry applies to
		// them all; a per-series list (built by the multi-target handler)
		// is indexed by position.
		path := s.name
		switch {
		case len(ro.PathExpressions) == 1 && ro.PathExpressions[0] != "":
			path = ro.PathExpressions[0]
		case i < len(ro.PathExpressions) && ro.PathExpressions[i] != "":
			path = ro.PathExpressions[i]
		}
		b = msgp.AppendString(b, path)
		b = msgp.AppendString(b, "valuesPerPoint")
		b = appendInt(b, 1)
		b = msgp.AppendString(b, "consolidationFunc")
		b = msgp.AppendString(b, consolidationFunc(s.tags))
		b = msgp.AppendString(b, "xFilesFactor")
		b = msgp.AppendFloat64(b, ro.XFilesFactor)
	}
	return b, nil
}

// appendArrayHeader writes an array header for n elements; n is a slice
// length and so never exceeds the uint32 range in practice
func appendArrayHeader(b []byte, n int) []byte {
	if n < 0 || n > math.MaxUint32 {
		n = math.MaxUint32
	}
	return msgp.AppendArrayHeader(b, uint32(n)) //nolint:gosec // bounded above
}

func appendInt(b []byte, v int64) []byte {
	if v >= 0 {
		return msgp.AppendUint64(b, uint64(v))
	}
	return msgp.AppendInt64(b, v)
}
