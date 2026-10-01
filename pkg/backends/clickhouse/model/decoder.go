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
	"fmt"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	trstr "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

const (
	// the delimiters of the tag names and values in a series' name, which no real tag holds
	tagDelim      = `/°|³\`
	tagPairsDelim = `/✓¿⨉\`
	// the log key for the number of rows dropped for their times
	logKeyRows = "rows"
	// a time given as a count of units since the epoch, by its digits: nanoseconds, then micro-,
	// milli- and whole seconds; fewer digits aren't a time
	nanoDigits  = 19
	microDigits = 16
	milliDigits = 13
	secDigits   = 10
	// the lengths of a date's text and of a DateTime's, before any fraction
	sqlDateLen = len("2006-01-02")
	sqlTimeLen = len("2006-01-02 15:04:05")
)

var errFieldCount = fmt.Errorf("%w: a row's field count differs from the header's", timeseries.ErrInvalidBody)

// decoder builds a DataSet from a response's rows: TabSeparatedWithNamesAndTypes text, or the
// blocks of the Native format, whose columns are typed as the same text would be
type decoder struct {
	trq *timeseries.TimeRangeQuery
	b   *dataset.Builder
	// the column names and types, until the fields are laid out from them
	header [dataStartRow][]string
	// the lines read, and the cells each holds
	lines, width int
	cells        [][]byte
	fields       timeseries.SeriesFields
	// the time, tag and value fields' columns
	timeCol          int
	tagCols, valCols []int
	scratch, name    []byte
	// rows dropped for their times, which are logged once
	dropped int
	dropErr error
	// the zone the response writes a DateTime in that has none of its own, and the time's and each
	// tag's and value's reading of its text
	zone     *time.Location
	timeZone *epoch.Zone
	tags     []valueText
	vals     []valueText
	timeBuf  []byte
}

// valueText is how a tag's or value's text is read beyond its field's type
type valueText struct {
	// text that's held as it's read, unless escaped
	plain bool
	// a FixedString, whose NUL padding is trimmed
	fixed bool
	// an Array, Map or Tuple, whose literal TSV writes as it is, its elements escaped within it
	raw bool
	// a DateTime, which is held as its UTC text, and the zone of its text when that isn't UTC
	time bool
	zone *epoch.Zone
}

func newDecoder(trq *timeseries.TimeRangeQuery, zone *time.Location) (*decoder, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	return &decoder{trq: trq, zone: zone}, nil
}

// newTSVDecoder returns a stream.Decoder for a TabSeparatedWithNamesAndTypes response
func newTSVDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	return tsvDecoderIn(nil)(trq)
}

// tsvDecoderIn returns a stream.NewDecoderFunc for TabSeparatedWithNamesAndTypes responses whose
// DateTimes without a zone of their own are written in zone, nil for UTC
func tsvDecoderIn(zone *time.Location) stream.NewDecoderFunc {
	return func(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
		d, err := newDecoder(trq, zone)
		if err != nil {
			return nil, err
		}
		return stream.NewLines(d.line, d.finishTSV), nil
	}
}

// line reads one line of TSV, which ClickHouse never quotes: its fields hold escapes instead of
// tabs and newlines. Blank lines are skipped.
func (d *decoder) line(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var escaped bool
	d.cells, escaped = splitTSV(raw, d.cells)
	if d.lines == 0 {
		d.width = len(d.cells)
	} else if len(d.cells) != d.width {
		return errFieldCount
	}
	d.lines++
	if d.lines > dataStartRow {
		return d.textRow(d.cells, escaped)
	}
	row := make([]string, len(d.cells))
	for i, cell := range d.cells {
		row[i] = string(d.unescape(cell))
	}
	d.header[d.lines-1] = row
	if d.lines < dataStartRow {
		return nil
	}
	return d.layout()
}

// layout sets the fields from the column names and types
func (d *decoder) layout() error {
	sf, err := buildFieldDefinitions(d.header[:], d.trq)
	if err != nil {
		return err
	}
	if sf.Timestamp.OutputPosition < 0 {
		return timeseries.ErrInvalidBody
	}
	d.fields, d.timeCol = sf, sf.Timestamp.OutputPosition
	opts := &FormatOptions{Zone: d.zone}
	d.timeZone = newOutField(&sf.Timestamp, opts).zone
	d.tagCols, d.tags = make([]int, len(sf.Tags)), make([]valueText, len(sf.Tags))
	for i := range sf.Tags {
		d.tagCols[i] = sf.Tags[i].OutputPosition
		d.tags[i] = newValueText(&sf.Tags[i], opts)
	}
	d.valCols, d.vals = make([]int, len(sf.Values)), make([]valueText, len(sf.Values))
	for i := range sf.Values {
		d.valCols[i] = sf.Values[i].OutputPosition
		d.vals[i] = newValueText(&sf.Values[i], opts)
	}
	d.b = dataset.NewBuilder(d.trq, dataset.BuilderOptions{Fields: sf, NameSeries: d.seriesName})
	return nil
}

func newValueText(fd *timeseries.FieldDefinition, opts *FormatOptions) valueText {
	f := newOutField(fd, opts)
	v := valueText{fixed: f.fixed > 0, raw: f.class == classCompound, time: f.class == classDateTime, zone: f.zone}
	v.plain = !v.fixed && !v.time
	return v
}

// seriesName names a series by its tags, in field order, as "." and each tag's name and value
func (d *decoder) seriesName(tags dataset.Tags) string {
	d.name = append(d.name[:0], '.')
	var n int
	for _, fd := range d.fields.Tags {
		v, ok := tags[fd.Name]
		if !ok {
			continue
		}
		if n > 0 {
			d.name = append(d.name, tagPairsDelim...)
		}
		d.name = append(append(append(d.name, fd.Name...), tagDelim...), v...)
		n++
	}
	return string(d.name)
}

// splitTSV splits a line at its tabs into dst, reporting whether the line holds an escape; a
// single pass beats a search for each of a line's short fields
func splitTSV(line []byte, dst [][]byte) ([][]byte, bool) {
	out, start, escaped := dst[:0], 0, false
	for i, c := range line {
		switch c {
		case '\t':
			out = append(out, line[start:i])
			start = i + 1
		case '\\':
			escaped = true
		}
	}
	return append(out, line[start:]), escaped
}

// textRow adds a row of TSV cells, which are unescaped when escaped; a row whose time doesn't
// parse is dropped
func (d *decoder) textRow(cells [][]byte, escaped bool) error {
	cell := func(i int) []byte {
		if escaped {
			return d.unescape(cells[i])
		}
		return cells[i]
	}
	e, err := d.textTime(cell(d.timeCol))
	if err != nil {
		d.drop(err)
		return nil
	}
	rb := d.b.Row()
	rb.SetEpoch(e)
	// a NULL tag is left out of the series' tags
	for i, c := range d.tagCols {
		if string(cells[c]) == nullToken {
			continue
		}
		text := cells[c]
		if escaped || !d.tags[i].plain {
			text = d.read(text, &d.tags[i], escaped)
		}
		rb.SetTag(i, text)
	}
	for i, c := range d.valCols {
		if string(cells[c]) == nullToken {
			rb.AddNull()
			continue
		}
		text := cells[c]
		if escaped || !d.vals[i].plain {
			text = d.read(text, &d.vals[i], escaped)
		}
		addText(rb, text, d.fields.Values[i].DataType)
	}
	return rb.Commit()
}

// read returns a cell's text as it's held: unescaped unless it's a compound's literal, a FixedString
// without its padding, and a DateTime as its UTC text
func (d *decoder) read(cell []byte, v *valueText, escaped bool) []byte {
	if escaped && !v.raw {
		cell = d.unescape(cell)
	}
	switch {
	case v.fixed:
		return bytes.TrimRight(cell, "\x00")
	case v.time:
		return d.utcText(cell, v.zone)
	}
	return cell
}

// utcText returns a DateTime's text as UTC text: ISO text's UTC, or text in zone shifted to UTC
func (d *decoder) utcText(text []byte, zone *epoch.Zone) []byte {
	if n := len(text); n > sqlTimeLen && text[n-1] == 'Z' && text[sqlDateLen] == 'T' {
		d.timeBuf = append(d.timeBuf[:0], text[:n-1]...)
		d.timeBuf[sqlDateLen] = ' '
		return d.timeBuf
	}
	if zone == nil {
		return text
	}
	e, ok := epoch.ParseSQLDateTime(text)
	if !ok {
		return text
	}
	digits := 0
	if len(text) > sqlTimeLen+1 {
		digits = len(text) - sqlTimeLen - 1
	}
	d.timeBuf = epoch.AppendSQLTime(d.timeBuf[:0], zone.FromLocal(e), ' ', digits)
	return d.timeBuf
}

func (d *decoder) drop(err error) {
	d.dropped++
	if d.dropErr == nil {
		d.dropErr = err
	}
}

// unescape returns cell without its TSV escapes, in the decoder's scratch buffer when it has any;
// the NULL literal \N is kept, for the value parser
func (d *decoder) unescape(cell []byte) []byte {
	if bytes.IndexByte(cell, '\\') < 0 || string(cell) == nullToken {
		return cell
	}
	d.scratch = appendUnescapedTSV(d.scratch[:0], cell)
	return d.scratch
}

// textTime parses the text of a time as its field's type reads it: a DateTime's ISO text is UTC, and
// its other text is in the column's zone
func (d *decoder) textTime(raw []byte) (epoch.Epoch, error) {
	raw = bytes.TrimSpace(raw)
	switch d.fields.Timestamp.DataType {
	case timeseries.DateTimeSQL:
		if e, ok := epoch.ParseSQLDateTime(raw); ok {
			if d.timeZone != nil {
				e = d.timeZone.FromLocal(e)
			}
			return e, nil
		}
		if e, ok := epoch.ParseCanonicalTime(raw, true); ok {
			return e, nil
		}
	case timeseries.DateSQL:
		if e, ok := epoch.ParseSQLDate(raw); ok {
			return e, nil
		}
	case timeseries.DateTimeRFC3339Nano:
		return epoch.ParseRFC3339(raw, time.RFC3339Nano)
	case timeseries.TimeSQL:
	default:
		if !trstr.IsApparentSQLDateFormat(string(raw)) {
			return epochFromDigits(raw)
		}
		if e, ok := epoch.ParseSQLDateTime(raw); ok {
			return e, nil
		}
	}
	return parseTimeField(string(raw), d.fields.Timestamp)
}

// epochFromDigits reads a time given as a count of units since the epoch, which its digits imply
func epochFromDigits(raw []byte) (epoch.Epoch, error) {
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, timeseries.ErrInvalidTimeFormat
		}
	}
	i, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, err
	}
	switch n := len(raw); {
	case n >= nanoDigits:
		return epoch.Epoch(i), nil
	case n >= microDigits:
		return epoch.Epoch(i * 1000), nil
	case n >= milliDigits:
		return epoch.Epoch(i * 1000000), nil
	case n >= secDigits:
		return epoch.Epoch(i * 1000000000), nil
	}
	return 0, timeseries.ErrInvalidTimeFormat
}

// addText adds a value's text as its field's type reads it: a number or bool that doesn't parse adds
// a null, and text, including a type that isn't read otherwise, is kept as it is
func addText(rb *dataset.RowBuilder, text []byte, dt timeseries.FieldDataType) {
	switch dt {
	case timeseries.Int64:
		if v, err := strconv.ParseInt(string(text), 10, 64); err == nil {
			rb.AddInt64(v)
			return
		}
	case timeseries.Uint64:
		if v, err := strconv.ParseUint(string(text), 10, 64); err == nil {
			rb.AddUint64(v)
			return
		}
	case timeseries.Float64:
		if v, err := strconv.ParseFloat(string(text), 64); err == nil {
			rb.AddFloat64(v)
			return
		}
	case timeseries.Bool:
		if v, err := strconv.ParseBool(string(text)); err == nil {
			rb.AddBool(v)
			return
		}
	case timeseries.Null:
	default:
		rb.AddString(text)
		return
	}
	rb.AddNull()
}

func (d *decoder) finishTSV() (timeseries.Timeseries, error) {
	if d.lines < dataStartRow {
		return nil, timeseries.ErrInvalidBody
	}
	return d.finish()
}

func (d *decoder) finish() (timeseries.Timeseries, error) {
	if d.dropped > 0 {
		logger.Error("failed to parse timestamp", logging.Pairs{keys.Error: d.dropErr, logKeyRows: d.dropped})
	}
	ds, err := d.b.Finish()
	if err != nil {
		return nil, err
	}
	// a response without rows has no results
	if len(ds.Results[0].SeriesList) == 0 {
		ds.Results = dataset.Results{}
	}
	return ds, nil
}
