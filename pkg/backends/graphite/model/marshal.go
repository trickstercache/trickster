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
	values []float64
	valid  []uint64 // validity bitmap: bit (i + bitOff) covers values[i]
	bitOff int      // consumed by consolidateFor's leading drop
	// consolidation state, JSON only
	valuesPerPoint int
}

// true when values[i] holds a real datapoint; false means null
func (s *series) isValid(i int) bool {
	i += s.bitOff
	return s.valid[i>>6]&(1<<(uint(i)&63)) != 0 //nolint:gosec // i is a bounded slice index
}

// Points are the buckets the origin returned (nulls included), so start, end
// and step come from them; a series with fewer than two points takes the query's step.
func flatten(ds *dataset.DataSet) []*series {
	var step int64
	if ds.TimeRangeQuery != nil {
		step = int64(ds.TimeRangeQuery.Step / time.Second)
	}
	n := 0
	for _, r := range ds.Results {
		if r != nil {
			n += len(r.SeriesList)
		}
	}
	out := make([]*series, 0, n)
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
				sr.values = make([]float64, len(s.Points))
				sr.valid = make([]uint64, (len(s.Points)+63)/64)
				for i, p := range s.Points {
					if len(p.Values) > 0 {
						if f, ok := p.Values[0].(float64); ok {
							sr.values[i] = f
							sr.valid[i>>6] |= 1 << (uint(i) & 63) //nolint:gosec // bounded index
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
	var buf bytes.Buffer
	n := 0
	for _, s := range all {
		n += len(s.values)
	}
	// ~26 bytes per rendered point plus per-series framing
	buf.Grow(n*26 + len(all)*128 + 2)
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
			vals, validAt := s.values, s.isValid
			if s.valuesPerPoint > 1 {
				cv, cb := s.consolidate(consolidationFunc(s.tags), ro.XFilesFactor)
				vals = cv
				validAt = func(i int) bool { return cb[i>>6]&(1<<(uint(i)&63)) != 0 } //nolint:gosec // bounded index
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
			isNull := func(i int) bool { return !validAt(i) || math.IsNaN(vals[i]) }
			if ro.NoNullPoints {
				any := false
				for i := range n {
					if !isNull(i) {
						any = true
						break
					}
				}
				if !any {
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
			var num []byte
			wrote := false
			for i := range n {
				null := isNull(i)
				if ro.NoNullPoints && null {
					continue
				}
				if wrote {
					buf.WriteString(", ")
				}
				wrote = true
				buf.WriteByte('[')
				if null {
					// graphite-web's JSON renders both a null datapoint
					// and a stored NaN as null
					buf.WriteString("null")
				} else if math.IsInf(vals[i], 0) {
					// graphite-web emits Infinity and then rewrites it
					if vals[i] > 0 {
						buf.WriteString("1e9999")
					} else {
						buf.WriteString("-Infinity")
					}
				} else {
					num = appendPyFloat(num[:0], vals[i])
					buf.Write(num)
				}
				buf.WriteString(", ")
				num = strconv.AppendInt(num[:0], s.start+int64(i)*span, 10)
				buf.Write(num)
				buf.WriteByte(']')
			}
			buf.WriteString("]}")
		}
	}
	buf.WriteByte(']')
	out := buf.Bytes()
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

// reproduces renderViewJson's maxDataPoints handling, including the start
// "nudge" that aligns bands across refreshes and its off-by-one (drops valuesToLose-1)
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
	if drop := int(nudge/s.step) - 1; drop > 0 {
		d := min(drop, len(s.values))
		s.values = s.values[d:]
		s.bitOff += d // the bitmap is addressed by original index
	}
	s.valuesPerPoint = valuesPerPoint
}

// Python's % for int64 (result has the divisor's sign)
func pyMod(a, b int64) int64 {
	m := a % b
	if m != 0 && (m < 0) != (b < 0) {
		m += b
	}
	return m
}

// the series' consolidation function: the consolidateBy tag graphite-web sets, else average
func consolidationFunc(tags dataset.Tags) string {
	if cf, ok := tags["consolidateBy"]; ok && cf != "" {
		return cf
	}
	return DefaultConsolidationFunc
}

// reproduces graphite-web's TimeSeries.__consolidatingGenerator
func (s *series) consolidate(cf string, xff float64) ([]float64, []uint64) {
	if cf == "avg" {
		cf = "average"
	}
	perPoint := s.valuesPerPoint
	n := (len(s.values) + perPoint - 1) / perPoint
	out := make([]float64, 0, n)
	bits := make([]uint64, (n+63)/64)
	buf := make([]float64, 0, perPoint)
	valcnt, nonNull := 0, 0
	flush := func() {
		if nonNull > 0 && float64(nonNull)/float64(perPoint) >= xff {
			i := len(out)
			bits[i>>6] |= 1 << (uint(i) & 63) //nolint:gosec // bounded index
			out = append(out, applyCF(cf, buf))
		} else {
			out = append(out, 0)
		}
		buf, valcnt, nonNull = buf[:0], 0, 0
	}
	for i, x := range s.values {
		valcnt++
		if s.isValid(i) {
			buf = append(buf, x)
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
	return out, bits
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

// renders the tags object: "name" first, then the rest sorted — the one place
// output can differ from graphite-web, which keeps Python tag-insertion order
func writeTags(buf *bytes.Buffer, tags dataset.Tags) {
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

// encodes a string as Python json.dumps with ensure_ascii=True: non-ASCII as
// \uXXXX (surrogate pairs above the BMP), control chars escaped, no escaping of < > &
func writePyJSONString(buf *bytes.Buffer, s string) {
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

func writeU(buf *bytes.Buffer, r rune) {
	const hex = "0123456789abcdef"
	buf.WriteString(`\u`)
	buf.WriteByte(hex[(r>>12)&0xf])
	buf.WriteByte(hex[(r>>8)&0xf])
	buf.WriteByte(hex[(r>>4)&0xf])
	buf.WriteByte(hex[r&0xf])
}

// PyFloat formats a float as Python's repr() does: shortest round-tripping digits,
// fixed notation for exponents in [-4, 16) with a trailing ".0", else scientific (1e+16)
func PyFloat(f float64) string {
	return string(appendPyFloat(nil, f))
}

func appendPyFloat(dst []byte, f float64) []byte {
	switch {
	case math.IsNaN(f):
		return append(dst, "nan"...)
	case math.IsInf(f, 1):
		return append(dst, "inf"...)
	case math.IsInf(f, -1):
		return append(dst, "-inf"...)
	case f == 0:
		if math.Signbit(f) {
			return append(dst, "-0.0"...)
		}
		return append(dst, "0.0"...)
	}
	var scratch [32]byte
	e := strconv.AppendFloat(scratch[:0], f, 'e', -1, 64) // [-]d[.ddd]e±XX
	if e[0] == '-' {
		dst = append(dst, '-')
		e = e[1:]
	}
	cut := bytes.IndexByte(e, 'e')
	mant, expStr := e[:cut], e[cut+1:]
	exp, _ := strconv.Atoi(string(expStr))
	// digits is the mantissa without its decimal point
	var dbuf [24]byte
	digits := dbuf[:0]
	for _, c := range mant {
		if c != '.' {
			digits = append(digits, c)
		}
	}
	switch {
	case exp < -4 || exp >= 16:
		dst = append(dst, mant...)
		dst = append(dst, 'e')
		dst = append(dst, expStr...)
	case exp < 0:
		dst = append(dst, "0."...)
		for range -exp - 1 {
			dst = append(dst, '0')
		}
		dst = append(dst, digits...)
	case len(digits) <= exp+1:
		dst = append(dst, digits...)
		for range exp + 1 - len(digits) {
			dst = append(dst, '0')
		}
		dst = append(dst, ".0"...)
	default:
		dst = append(dst, digits[:exp+1]...)
		dst = append(dst, '.')
		dst = append(dst, digits[exp+1:]...)
	}
	return dst
}

// ---------------------------------------------------------------------------
// raw (renderViewRaw): <name>,<start>,<end>,<step>|repr,repr,None\n

func renderRaw(all []*series) []byte {
	var buf bytes.Buffer
	n := 0
	for _, s := range all {
		n += len(s.values)
	}
	buf.Grow(n*9 + len(all)*96)
	for _, s := range all {
		buf.WriteString(s.name)
		buf.WriteByte(',')
		buf.WriteString(strconv.FormatInt(s.start, 10))
		buf.WriteByte(',')
		buf.WriteString(strconv.FormatInt(s.end, 10))
		buf.WriteByte(',')
		buf.WriteString(strconv.FormatInt(s.step, 10))
		buf.WriteByte('|')
		var num []byte
		for i, v := range s.values {
			if i > 0 {
				buf.WriteByte(',')
			}
			if !s.isValid(i) {
				buf.WriteString("None")
			} else {
				num = appendPyFloat(num[:0], v)
				buf.Write(num)
			}
		}
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// csv (renderViewCsv): name,YYYY-MM-DD HH:MM:SS,value\r\n in the request tz

func renderCSV(all []*series, ro RenderOptions) []byte {
	loc := ro.Location
	if loc == nil {
		loc = time.UTC
	}
	var buf bytes.Buffer
	n := 0
	for _, s := range all {
		n += len(s.values) * (len(s.name) + 34)
	}
	buf.Grow(n)
	var num []byte
	for _, s := range all {
		for i, v := range s.values {
			csvField(&buf, s.name)
			buf.WriteByte(',')
			num = time.Unix(s.start+int64(i)*s.step, 0).In(loc).AppendFormat(num[:0], "2006-01-02 15:04:05")
			buf.Write(num)
			buf.WriteByte(',')
			if s.isValid(i) {
				num = appendPyFloat(num[:0], v)
				buf.Write(num)
			}
			buf.WriteString("\r\n")
		}
	}
	return buf.Bytes()
}

// quotes a field as Python's csv excel dialect does
func csvField(buf *bytes.Buffer, s string) {
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
	n := 0
	for _, s := range all {
		n += 9*len(s.values) + len(s.name) + 160
	}
	b := make([]byte, 0, n+8)
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
		for i, v := range s.values {
			if !s.isValid(i) {
				b = msgp.AppendNil(b)
			} else {
				b = msgp.AppendFloat64(b, v)
			}
		}
		b = msgp.AppendString(b, "pathExpression")
		// pathExpression is the target expression that produced the series;
		// a single entry covers every series, a per-series list is indexed by position
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

// n is a slice length and so never exceeds the uint32 range in practice
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
