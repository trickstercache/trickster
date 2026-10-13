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

package global

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

// Prometheus HTTP API error types
const (
	ErrorTypeBadData     = "bad_data"
	ErrorTypeExecution   = "execution"
	ErrorTypeTimeout     = "timeout"
	ErrorTypeCanceled    = "canceled"
	ErrorTypeInternal    = "internal"
	ErrorTypeUnavailable = "unavailable"
)

const (
	contentTypeJSON = "application/json"
	// maxPooledResponse keeps the response buffers of unusually large answers from staying in the pool
	maxPooledResponse = 4 << 20
	// encodeSlack is the most bytes encoded between two checks of an encoding's size, as a sample or a histogram
	// bucket with the punctuation around it; a response buffer grows to at most its limit plus this
	encodeSlack = 256
	// minErrorBytes is the least an error response may take, so a result limit too small for its message still leaves it
	minErrorBytes = 4 << 10
	// growThreshold is the capacity from which a response buffer grows by about a quarter rather than doubling, as append grows
	growThreshold = 256
	// quotedPlain, quotedShort and quotedUnicode are the lengths of a byte that JSON quotes as it is, of a backslash escape,
	// and of a \u escape
	quotedPlain   = 1
	quotedShort   = 2
	quotedUnicode = 6
	// hexDigits are the digits of a \u escape, lowercase as encoding/json writes them
	hexDigits = "0123456789abcdef"
	// replacementChar is written for each byte of invalid UTF-8, as encoding/json writes it
	replacementChar = string(utf8.RuneError)
)

// ErrResultTooLarge is returned when an evaluation's encoded result exceeds the bytes allowed it.
var ErrResultTooLarge = errors.New("tsm global evaluation result exceeded its size limit")

var errBodyClosed = errors.New("read on a closed result body")

var responseBuffers = sync.Pool{New: func() any { return new([]byte) }}

// asciiQuoted holds the length of each ASCII byte as encoding/json quotes it, escaping HTML characters
var asciiQuoted = func() (lens [utf8.RuneSelf]uint8) {
	for c := range lens {
		switch {
		case c == '"' || c == '\\' || c == '\b' || c == '\f' || c == '\n' || c == '\r' || c == '\t':
			lens[c] = quotedShort
		case c < ' ' || c == '<' || c == '>' || c == '&':
			lens[c] = quotedUnicode
		default:
			lens[c] = quotedPlain
		}
	}
	return lens
}()

// ResultLimitError returns the error an evaluation fails with when its encoded result exceeds maxBytes.
func ResultLimitError(maxBytes int64) error {
	return fmt.Errorf("%w: more than %d bytes", ErrResultTooLarge, maxBytes)
}

// BodyKeeper is a response writer that keeps a result's encoded body as it is rather than copying it. Reading the
// body to its end, or closing it, returns the body's buffer for reuse.
type BodyKeeper interface {
	KeepBody(body io.ReadCloser, size int)
}

// WriteResult writes an engine result as the Prometheus HTTP API returns it, unless its encoding takes more than
// maxBytes, zero for no limit: then it writes nothing and returns ErrResultTooLarge, having stopped encoding once past the limit.
func WriteResult(w http.ResponseWriter, value parser.Value, warnings, infos []string, maxBytes int64) error {
	buf := responseBuffers.Get().(*[]byte)
	e := resultEncoder{b: (*buf)[:0], limit: encodeLimit(maxBytes)}
	if !e.result(value, warnings, infos) {
		putResponseBuffer(buf, e.b)
		return ResultLimitError(maxBytes)
	}
	w.Header().Set(headers.NameContentType, contentTypeJSON)
	w.WriteHeader(http.StatusOK)
	if k, ok := w.(BodyKeeper); ok {
		*buf = e.b
		k.KeepBody(&resultBody{buf: buf}, len(e.b))
		return nil
	}
	_, _ = w.Write(e.b)
	putResponseBuffer(buf, e.b)
	return nil
}

// WriteError writes a Prometheus HTTP API error response of at most maxBytes, zero for no limit, or 4 KiB when that is
// more: warnings that don't fit are left out, and a message that doesn't is cut short.
func WriteError(w http.ResponseWriter, status int, errorType string, err error, warnings []string, maxBytes int64) {
	limit := max(encodeLimit(maxBytes), minErrorBytes)
	e := resultEncoder{limit: encodeLimit(0)}
	e.room(0)
	e.b = append(e.b, `{"status":"error","errorType":`...)
	e.str(errorType)
	e.room(0)
	e.b = append(e.b, `,"error":`...)
	// the message leaves room for the closing brace
	msg := err.Error()
	cut, n := quotedPrefix(msg, limit-len(e.b)-1)
	e.room(n)
	e.b = appendQuoted(e.b, msg[:cut], 0)
	if len(warnings) > 0 {
		e.limit = limit - 1
		e.fitStrings("warnings", warnings)
		e.limit = encodeLimit(0)
	}
	e.room(1)
	e.b = append(e.b, '}')
	w.Header().Set(headers.NameContentType, contentTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(e.b)
}

// putResponseBuffer returns buf to the pool holding b, unless an unusually large answer grew b, which is left to the collector
func putResponseBuffer(buf *[]byte, b []byte) {
	if cap(b) <= maxPooledResponse {
		*buf = b[:0]
		responseBuffers.Put(buf)
	}
}

// resultBody reads an encoded result from its pooled buffer, which it returns to the pool once read to its end or
// closed; reads and the close can come from different goroutines, as when a stalled transfer is closed
type resultBody struct {
	mu  sync.Mutex
	buf *[]byte
	off int
	// done records that the whole result was read, so later reads find its end rather than a closed body
	done bool
}

func (r *resultBody) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buf == nil {
		if r.done {
			return 0, io.EOF
		}
		return 0, errBodyClosed
	}
	b := *r.buf
	n := copy(p, b[r.off:])
	r.off += n
	if r.off < len(b) {
		return n, nil
	}
	// a reader needn't close what it read to its end, as a cache filling from it doesn't, so the end returns the buffer
	putResponseBuffer(r.buf, b)
	r.buf, r.done = nil, true
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (r *resultBody) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buf != nil {
		putResponseBuffer(r.buf, *r.buf)
		r.buf = nil
	}
	return nil
}

// resultEncoder appends an encoding to b, which grows to at most limit plus encodeSlack bytes
type resultEncoder struct {
	b     []byte
	limit int
}

// encodeLimit returns maxBytes as an encoder's limit, which is as high as one can be when maxBytes sets none
func encodeLimit(maxBytes int64) int {
	if maxBytes <= 0 || maxBytes > math.MaxInt-encodeSlack {
		return math.MaxInt - encodeSlack
	}
	return int(maxBytes)
}

// room readies b for n more bytes, and up to encodeSlack after them, reporting false when the n would take the
// encoding past its limit; growth stops at the limit plus the slack, so an encoding too large allocates no more
func (e *resultEncoder) room(n int) bool {
	need := len(e.b) + n
	if need > e.limit {
		return false
	}
	if need+encodeSlack <= cap(e.b) {
		return true
	}
	c := cap(e.b)
	if c < growThreshold {
		c *= 2
	} else {
		c += (c + 3*growThreshold) / 4
	}
	c = min(max(c, need+encodeSlack), e.limit+encodeSlack)
	b := make([]byte, len(e.b), c)
	copy(b, e.b)
	e.b = b
	return true
}

// result encodes a successful response, reporting false once it exceeds the limit
func (e *resultEncoder) result(value parser.Value, warnings, infos []string) bool {
	if !e.room(0) {
		return false
	}
	e.b = append(e.b, `{"status":"success","data":{"resultType":"`...)
	e.b = append(e.b, value.Type()...)
	e.b = append(e.b, `","result":`...)
	if !e.value(value) || !e.room(0) {
		return false
	}
	e.b = append(e.b, '}')
	if !e.strings("warnings", warnings) || !e.strings("infos", infos) || !e.room(1) {
		return false
	}
	e.b = append(e.b, '}')
	return true
}

func (e *resultEncoder) strings(field string, values []string) bool {
	if len(values) == 0 {
		return true
	}
	if !e.room(0) {
		return false
	}
	e.b = append(e.b, `,"`...)
	e.b = append(e.b, field...)
	e.b = append(e.b, `":[`...)
	for i, v := range values {
		if i > 0 {
			e.b = append(e.b, ',')
		}
		if !e.str(v) {
			return false
		}
	}
	if !e.room(0) {
		return false
	}
	e.b = append(e.b, ']')
	return true
}

// fitStrings appends a field of the values that fit within the limit, in order, leaving the field out when none does
func (e *resultEncoder) fitStrings(field string, values []string) {
	start := len(e.b)
	if !e.room(len(field) + len(`,"":[]`)) {
		return
	}
	e.b = append(e.b, `,"`...)
	e.b = append(e.b, field...)
	e.b = append(e.b, `":[`...)
	listed := len(e.b)
	// values leave room for the closing bracket
	e.limit--
	for _, v := range values {
		mark := len(e.b)
		if mark > listed {
			if !e.room(1) {
				break
			}
			e.b = append(e.b, ',')
		}
		if !e.str(v) {
			e.b = e.b[:mark]
		}
	}
	e.limit++
	if len(e.b) == listed {
		e.b = e.b[:start]
		return
	}
	e.b = append(e.b, ']')
}

func (e *resultEncoder) value(value parser.Value) bool {
	if !e.room(0) {
		return false
	}
	switch v := value.(type) {
	case promql.Matrix:
		e.b = append(e.b, '[')
		for i := range v {
			if !e.room(0) {
				return false
			}
			if i > 0 {
				e.b = append(e.b, ',')
			}
			if !e.series(&v[i]) {
				return false
			}
		}
	case promql.Vector:
		e.b = append(e.b, '[')
		for i := range v {
			if !e.room(0) {
				return false
			}
			if i > 0 {
				e.b = append(e.b, ',')
			}
			if !e.sample(&v[i]) {
				return false
			}
		}
	case promql.Scalar:
		e.b = appendFloatPoint(e.b, v.T, v.V)
		return true
	case promql.String:
		e.b = append(e.b, '[')
		e.b = appendTimestamp(e.b, v.T)
		e.b = append(e.b, ',')
		if !e.str(v.V) {
			return false
		}
	default:
		e.b = append(e.b, "null"...)
		return true
	}
	if !e.room(0) {
		return false
	}
	e.b = append(e.b, ']')
	return true
}

func (e *resultEncoder) series(series *promql.Series) bool {
	if !e.metric(series.Metric) {
		return false
	}
	if len(series.Floats) > 0 {
		e.b = append(e.b, `,"values":[`...)
		for j, p := range series.Floats {
			if !e.room(0) {
				return false
			}
			if j > 0 {
				e.b = append(e.b, ',')
			}
			e.b = appendFloatPoint(e.b, p.T, p.F)
		}
		if !e.room(0) {
			return false
		}
		e.b = append(e.b, ']')
	}
	if len(series.Histograms) > 0 {
		e.b = append(e.b, `,"histograms":[`...)
		for j, p := range series.Histograms {
			if !e.room(0) {
				return false
			}
			if j > 0 {
				e.b = append(e.b, ',')
			}
			if !e.histogramPoint(p.T, p.H) {
				return false
			}
		}
		if !e.room(0) {
			return false
		}
		e.b = append(e.b, ']')
	}
	e.b = append(e.b, '}')
	return true
}

func (e *resultEncoder) sample(s *promql.Sample) bool {
	if !e.metric(s.Metric) {
		return false
	}
	if s.H != nil {
		e.b = append(e.b, `,"histogram":`...)
		if !e.histogramPoint(s.T, s.H) {
			return false
		}
	} else {
		e.b = append(e.b, `,"value":`...)
		e.b = appendFloatPoint(e.b, s.T, s.F)
	}
	e.b = append(e.b, '}')
	return true
}

// metric encodes the opening of a series or sample, with its labels
func (e *resultEncoder) metric(lset labels.Labels) bool {
	if !e.room(0) {
		return false
	}
	e.b = append(e.b, `{"metric":{`...)
	ok, first := true, true
	lset.Range(func(l labels.Label) {
		if !ok {
			return
		}
		if !first {
			e.b = append(e.b, ',')
		}
		first = false
		if ok = e.str(l.Name); ok {
			e.b = append(e.b, ':')
			ok = e.str(l.Value)
		}
	})
	if !ok || !e.room(0) {
		return false
	}
	e.b = append(e.b, '}')
	return true
}

func appendFloatPoint(b []byte, t int64, f float64) []byte {
	b = append(b, '[')
	b = appendTimestamp(b, t)
	b = append(b, ',')
	b = appendFloat(b, f)
	return append(b, ']')
}

// appendTimestamp writes milliseconds as fractional Unix seconds, as Prometheus does
func appendTimestamp(b []byte, t int64) []byte {
	if t < 0 {
		b = append(b, '-')
		t = -t
	}
	b = strconv.AppendInt(b, t/1000, 10)
	if fraction := t % 1000; fraction != 0 {
		b = append(b, '.')
		if fraction < 100 {
			b = append(b, '0')
		}
		if fraction < 10 {
			b = append(b, '0')
		}
		b = strconv.AppendInt(b, fraction, 10)
	}
	return b
}

// appendFloat writes a quoted sample value as Prometheus does, exponent notation for very small or large values
func appendFloat(b []byte, f float64) []byte {
	b = append(b, '"')
	format := byte('f')
	if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, 64)
	return append(b, '"')
}

// histogramPoint writes a native histogram sample's non-empty buckets as boundary rule (0 lower
// exclusive, 1 upper exclusive, 2 both, 3 neither), boundaries and count
func (e *resultEncoder) histogramPoint(t int64, h *histogram.FloatHistogram) bool {
	e.b = append(e.b, '[')
	e.b = appendTimestamp(e.b, t)
	e.b = append(e.b, `,{"count":`...)
	e.b = appendFloat(e.b, h.Count)
	e.b = append(e.b, `,"sum":`...)
	e.b = appendFloat(e.b, h.Sum)
	found := false
	for it := h.AllBucketIterator(); it.Next(); {
		bucket := it.At()
		if bucket.Count == 0 {
			continue
		}
		if !e.room(0) {
			return false
		}
		if !found {
			e.b = append(e.b, `,"buckets":[`...)
			found = true
		} else {
			e.b = append(e.b, ',')
		}
		rule := 2
		switch {
		case bucket.LowerInclusive && bucket.UpperInclusive:
			rule = 3
		case bucket.LowerInclusive:
			rule = 1
		case bucket.UpperInclusive:
			rule = 0
		}
		e.b = append(e.b, '[')
		e.b = strconv.AppendInt(e.b, int64(rule), 10)
		e.b = append(e.b, ',')
		e.b = appendFloat(e.b, bucket.Lower)
		e.b = append(e.b, ',')
		e.b = appendFloat(e.b, bucket.Upper)
		e.b = append(e.b, ',')
		e.b = appendFloat(e.b, bucket.Count)
		e.b = append(e.b, ']')
	}
	if !e.room(0) {
		return false
	}
	if found {
		e.b = append(e.b, ']')
	}
	e.b = append(e.b, "}]"...)
	return true
}

// str appends s quoted as encoding/json quotes it, reporting false when it would take the encoding past its limit
func (e *resultEncoder) str(s string) bool {
	// escaping never shortens a string, so one that can't fit as it is isn't measured
	avail := e.limit - len(e.b)
	if len(s)+2 > avail {
		return false
	}
	n, first := quotedLen(s, avail)
	if !e.room(n) {
		return false
	}
	e.b = appendQuoted(e.b, s, first)
	return true
}

// quotedLen returns the length of s quoted as encoding/json quotes it, or a length past limit once it passes limit, and the
// offset of its first byte that is escaped or not ASCII, len(s) when none is
func quotedLen(s string, limit int) (int, int) {
	first := 0
	for first < len(s) && s[first] < utf8.RuneSelf && asciiQuoted[s[first]] == quotedPlain {
		first++
	}
	n := len(s) + 2
	for i := first; i < len(s) && n <= limit; {
		if c := s[i]; c < utf8.RuneSelf {
			n += int(asciiQuoted[c]) - quotedPlain
			i++
			continue
		}
		size, quoted := quotedChar(s, i)
		n += quoted - size
		i += size
	}
	return n, first
}

// quotedPrefix returns how many leading bytes of s, ending between characters, quote within limit bytes, and their quoted length
func quotedPrefix(s string, limit int) (int, int) {
	i, n := 0, 2
	for i < len(s) {
		size, quoted := quotedChar(s, i)
		if n+quoted > limit {
			break
		}
		n += quoted
		i += size
	}
	return i, n
}

// quotedChar returns the size of the character at s[i], an invalid byte alone, and its length as encoding/json quotes it
func quotedChar(s string, i int) (int, int) {
	if c := s[i]; c < utf8.RuneSelf {
		return quotedPlain, int(asciiQuoted[c])
	}
	switch r, size := utf8.DecodeRuneInString(s[i:]); {
	case size == 1:
		return size, len(replacementChar)
	case r == '\u2028' || r == '\u2029':
		return size, quotedUnicode
	default:
		return size, size
	}
}

// appendQuoted appends s quoted as encoding/json quotes it, escaping HTML characters and the line and paragraph separators
// and replacing each invalid byte; nothing in s before first is escaped
func appendQuoted(b []byte, s string, first int) []byte {
	b = append(b, '"')
	start := 0
	for i := first; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if asciiQuoted[c] == quotedPlain {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"', '\\':
				b = append(b, '\\', c)
			case '\b':
				b = append(b, '\\', 'b')
			case '\f':
				b = append(b, '\\', 'f')
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if size == 1 || r == '\u2028' || r == '\u2029' {
			b = append(b, s[start:i]...)
			if size == 1 {
				b = append(b, replacementChar...)
			} else {
				b = append(b, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
			}
			start = i + size
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}
