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
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// OutputFormatNative is the output format index for ClickHouse Native binary.
const OutputFormatNative byte = 6

const (
	// LowCardinality's serialization, as clickhouse-go writes it: the dictionary is sent whole,
	// its keys are as wide as its size needs, and it starts with a default entry, or two when nullable
	lowCardinalityUpdateAll = 1<<9 | 1<<10
	lowCardinalityKey16     = 1
	lowCardinalityKey32     = 2
	lowCardinalityKey64     = 3
	nsPerDay                = secondsPerDay * int64(time.Second)
	contentTypeNative       = "application/octet-stream"
)

var (
	nativeOutputPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}
	nativeOutPool    = sync.Pool{New: func() any { return &nativeOuts{} }}
)

// nativeOuts are a marshal's column writers, whose buffers are reused by the next marshal
type nativeOuts struct {
	outs []nativeOut
}

// get returns n writers, each holding its buffers from an earlier marshal
func (nb *nativeOuts) get(n int) []nativeOut {
	for len(nb.outs) < n {
		nb.outs = append(nb.outs, nativeOut{})
	}
	return nb.outs[:n]
}

// release returns the writers for reuse, unless one holds more than a response should keep
func (nb *nativeOuts) release() {
	for i := range nb.outs {
		o := &nb.outs[i]
		if cap(o.data)+cap(o.nulls)+8*cap(o.keys) > maxPooledInput {
			return
		}
		*o = nativeOut{data: o.data[:0], nulls: o.nulls[:0], keys: o.keys[:0]}
	}
	nativeOutPool.Put(nb)
}

// marshalTimeseriesNative writes a DataSet as a Native block: a common type's column from the rows,
// and any other, or one holding a value its writer can't take, boxed for clickhouse-go.
func marshalTimeseriesNative(w io.Writer, ds *dataset.DataSet, options *timeseries.RequestOptions) error {
	revision := uint64(server.ServerRevision)
	zone := utcName
	if options != nil {
		if format, ok := options.ProviderRequest.(FormatOptions); ok {
			revision, zone = format.Revision, format.ZoneName()
		}
	}
	if hw, ok := w.(http.ResponseWriter); ok {
		hw.Header().Set(formatHeader, formatNative)
		hw.Header().Set(headers.NameContentType, contentTypeNative)
		hw.Header().Set(TimezoneHeader, zone)
	}
	// ClickHouse answers a query without rows with an empty body
	if len(ds.Results) == 0 || len(ds.Results[0].SeriesList) == 0 {
		return nil
	}
	fields, _, _, _ := ds.FieldDefinitions()
	rows, count := timeOrderedRows(ds.Results[0])
	columns := make([]server.Column, len(fields))
	nb := nativeOutPool.Get().(*nativeOuts)
	defer nb.release()
	outs := nb.get(len(fields))
	var boxed bool
	for i, f := range fields {
		columns[i] = server.Column{Name: f.Name, Type: f.SDataType}
		outs[i].init(f, count)
		boxed = boxed || outs[i].kind == outBoxed
	}
	layouts := make([]*nativeSeries, len(ds.Results[0].SeriesList))
	for row := range rows {
		layout := layouts[row.list]
		if layout == nil {
			layout = newNativeSeries(row.series, fields)
			layouts[row.list] = layout
		}
		for i := range outs {
			o := &outs[i]
			if o.role == timeseries.RoleValue && (layout.index[i] < 0 || layout.index[i] >= row.seg.NumCols()) {
				return timeseries.ErrInvalidBody
			}
			if o.kind != outBoxed {
				o.add(row, layout, i)
				boxed = boxed || o.kind == outBoxed
			}
		}
	}
	// the boxed columns are read in a second pass, as a column can turn boxed on its last row
	if boxed {
		for row := range rows {
			for i := range outs {
				if outs[i].kind == outBoxed {
					outs[i].values = append(outs[i].values, outs[i].boxedValue(row, layouts[row.list], i))
				}
			}
		}
	}
	// encode to memory first so a column that cannot be encoded yields an
	// error instead of a truncated body behind an already-sent status
	buf := nativeOutputPool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		nativeOutputPool.Put(buf)
	}()
	err := server.EncodeNativeFormatColumns(buf, columns, uint64(count), revision, //nolint:gosec // a row count
		func(i int, w io.Writer) error { return outs[i].encode(w, columns[i].Type, revision) })
	if err != nil {
		return err
	}
	_, err = w.Write(buf.Bytes())
	return err
}

// outKind is how a nativeOut writes its column
type outKind uint8

const (
	// boxed: the values are boxed for clickhouse-go to write
	outBoxed outKind = iota
	outInt
	outUint
	outFloat64
	outBool
	outString
	outLowCardinality
	// the floor of a time's nanoseconds divided by the column's scale
	outTime
)

// nativeOut writes one column of a block, as clickhouse-go writes the same values boxed
type nativeOut struct {
	kind     outKind
	role     timeseries.FieldRole
	nullable bool
	// the bytes of a fixed-width value, and the range of an integer's values
	size     int
	min, max int64
	umax     uint64
	// a time's nanoseconds per unit of the column, and whether its values wrap to the column's width
	// rather than fitting its range
	scale int64
	wraps bool
	// the dictionary starts with NULL
	dictNullable bool
	// how the time column's value is written as text, for the boxed column
	time  nativeTimeFormat
	nulls []byte
	data  []byte
	// a dictionary's entries after its defaults, by value, and each row's key
	dict    map[string]uint64
	entries uint64
	keys    []uint64
	values  []any
}

func (o *nativeOut) init(f timeseries.FieldDefinition, rows int) {
	o.role = f.Role
	typ := f.SDataType
	switch f.Role {
	case timeseries.RoleTimestamp:
		o.time = newNativeTimeFormat(f)
		o.initTime(f, typ)
	case timeseries.RoleTag, timeseries.RoleValue:
		if inner, ok := strings.CutPrefix(typ, prefixNullable); ok && strings.HasSuffix(inner, ")") {
			o.nullable, typ = true, inner[:len(inner)-1]
		}
		o.initValue(typ, f.Role == timeseries.RoleValue)
		if o.kind == outBoxed && f.Role == timeseries.RoleValue && strings.HasPrefix(typ, TypeDateTime) {
			// a DateTime value is its UTC text, written as its ticks
			if of := newOutField(&timeseries.FieldDefinition{SDataType: typ}, &FormatOptions{}); of.class == classDateTime {
				o.initTicks(of.precision, strings.HasPrefix(typ, prefixDateTime64))
			}
		}
	}
	switch o.kind {
	case outBoxed:
		o.values = make([]any, 0, rows)
	case outLowCardinality:
		o.dict, o.keys = make(map[string]uint64), slices.Grow(o.keys[:0], rows)
	case outString:
		o.data = slices.Grow(o.data[:0], 8*rows)
	default:
		o.data = slices.Grow(o.data[:0], o.size*rows)
	}
	if o.nullable {
		o.nulls = slices.Grow(o.nulls[:0], rows)
	}
}

func (o *nativeOut) initValue(typ string, value bool) {
	switch typ {
	case TypeString:
		o.kind = outString
	case "LowCardinality(String)", "LowCardinality(Nullable(String))":
		// a Nullable wrapper is the only one whose tags are NULLs; a nullable dictionary's aren't
		if !o.nullable {
			o.kind, o.dictNullable = outLowCardinality, strings.HasSuffix(typ, "))")
		}
	}
	if !value || o.kind != outBoxed {
		return
	}
	switch typ {
	case TypeFloat64:
		o.kind, o.size = outFloat64, 8
	case TypeBool:
		o.kind, o.size = outBool, 1
	default:
		o.initInt(typ)
	}
}

// initInt sets an integer type's width and range, leaving other types boxed
func (o *nativeOut) initInt(typ string) {
	bits := map[string]int{
		TypeInt8: 8, TypeInt16: 16, TypeInt32: 32, TypeInt64: 64,
		TypeUInt8: 8, TypeUInt16: 16, TypeUInt32: 32, TypeUInt64: 64,
	}[typ]
	if bits == 0 {
		return
	}
	o.size = bits / 8
	if typ[0] == 'U' {
		o.kind, o.umax = outUint, math.MaxUint64>>(64-bits)
		o.min, o.max = 0, int64(min(o.umax, math.MaxInt64)) //nolint:gosec // bounded
		return
	}
	o.kind, o.max = outInt, math.MaxInt64>>(64-bits)
	o.min = -o.max - 1
}

// initTicks sets a DateTime's writing as its ticks: a DateTime's seconds, or a DateTime64's units of its
// precision, whatever the column's zone, as the ticks are UTC's
func (o *nativeOut) initTicks(precision int, wide bool) {
	if precision < 0 || precision > maxPrecision {
		return
	}
	o.kind, o.wraps = outTime, true
	if wide {
		o.size, o.scale = 8, pow10[maxPrecision-precision]
		return
	}
	o.size, o.scale = 4, int64(time.Second)
}

// initTime sets how the time column is written. clickhouse-go reads the time's text: a DateTime is
// its seconds, a Date its days, and a DateTime64 its ticks; a time given in units is its integer.
func (o *nativeOut) initTime(f timeseries.FieldDefinition, typ string) {
	o.kind = outTime
	of := newOutField(&f, &FormatOptions{})
	switch {
	case strings.HasPrefix(typ, TypeDateTime):
		o.kind = outBoxed
		o.initTicks(of.precision, strings.HasPrefix(typ, prefixDateTime64))
	case typ == TypeDate:
		o.size, o.scale, o.wraps = 2, nsPerDay, true
	case o.time.layout == "":
		o.kind = outBoxed
		o.initInt(typ)
		if o.kind == outBoxed {
			return
		}
		o.kind = outTime
		switch f.DataType {
		case timeseries.DateTimeUnixMilli:
			o.scale = int64(time.Millisecond)
		case timeseries.DateTimeUnixMicro:
			o.scale = int64(time.Microsecond)
		case timeseries.DateTimeUnixNano:
			o.scale = 1
		default:
			o.scale = int64(time.Second)
		}
	default:
		o.kind = outBoxed
	}
}

// add writes a row's value, or turns the column boxed when its writer doesn't take the value
func (o *nativeOut) add(row outputRow, layout *nativeSeries, i int) {
	switch o.role {
	case timeseries.RoleTimestamp:
		o.addTime(row.epoch())
		return
	case timeseries.RoleTag:
		o.addTag(layout, i)
		return
	}
	seg, c, r := row.seg, layout.index[i], row.i
	kind := seg.KindAt(c, r)
	if kind == dataset.KindNull {
		o.addNull()
		return
	}
	switch {
	case o.kind == outFloat64 && kind == dataset.KindFloat64:
		v := seg.Float64(c, r)
		// its text reads as the one NaN
		if math.IsNaN(v) {
			v = math.NaN()
		}
		o.present()
		o.data = binary.LittleEndian.AppendUint64(o.data, math.Float64bits(v))
	case o.kind == outInt && kind == dataset.KindInt64 && seg.Int64(c, r) >= o.min && seg.Int64(c, r) <= o.max:
		o.present()
		o.appendWord(uint64(seg.Int64(c, r))) //nolint:gosec // two's complement
	case o.kind == outUint && kind == dataset.KindUint64 && seg.Uint64(c, r) <= o.umax:
		o.present()
		o.appendWord(seg.Uint64(c, r))
	case o.kind == outBool && kind == dataset.KindBool:
		o.present()
		o.data = append(o.data, boolByte(seg.Bool(c, r)))
	case o.kind == outString && kind == dataset.KindString:
		o.present()
		o.data = appendNativeString(o.data, seg.Bytes(c, r))
	case o.kind == outLowCardinality && kind == dataset.KindString:
		o.keys = append(o.keys, dictKey(o, seg.Bytes(c, r)))
	case o.kind == outTime && kind == dataset.KindString:
		e, ok := epoch.ParseSQLDateTime(seg.Bytes(c, r))
		if !ok {
			o.box()
			return
		}
		o.present()
		o.addTime(e)
	default:
		o.box()
	}
}

func (o *nativeOut) addTime(e epoch.Epoch) {
	v := int64(e) / o.scale
	if int64(e)%o.scale < 0 {
		v--
	}
	if !o.wraps && (v < o.min || v > o.max) {
		o.box()
		return
	}
	o.appendWord(uint64(v)) //nolint:gosec // two's complement, truncated to the column's width
}

// addTag writes a series' tag, which is its text, or NULL for a Nullable column
func (o *nativeOut) addTag(layout *nativeSeries, i int) {
	s, ok := layout.cells[i].(string)
	switch {
	case !ok:
		o.addNull()
	case o.kind == outString:
		o.present()
		o.data = appendNativeString(o.data, s)
	default:
		// a series' tag has one key, found once
		if layout.keys[i] == noKey {
			layout.keys[i] = dictKey(o, s)
		}
		o.keys = append(o.keys, layout.keys[i])
	}
}

// addNull writes a NULL, which a type that isn't nullable writes as its zero value
func (o *nativeOut) addNull() {
	switch o.kind {
	case outLowCardinality:
		o.keys = append(o.keys, 0)
		return
	case outString:
		o.data = append(o.data, 0)
	case outInt, outUint, outFloat64, outBool, outTime:
		o.data = append(o.data, make([]byte, o.size)...)
	default:
		o.box()
		return
	}
	if o.nullable {
		o.nulls = append(o.nulls, 1)
	}
}

func (o *nativeOut) present() {
	if o.nullable {
		o.nulls = append(o.nulls, 0)
	}
}

func (o *nativeOut) appendWord(v uint64) {
	switch o.size {
	case 1:
		o.data = append(o.data, byte(v)) //nolint:gosec // truncated
	case 2:
		o.data = binary.LittleEndian.AppendUint16(o.data, uint16(v)) //nolint:gosec // truncated
	case 4:
		o.data = binary.LittleEndian.AppendUint32(o.data, uint32(v)) //nolint:gosec // truncated
	default:
		o.data = binary.LittleEndian.AppendUint64(o.data, v)
	}
}

// appendNativeString appends a String value: its length, then its bytes
func appendNativeString[T ~string | ~[]byte](dst []byte, v T) []byte {
	return append(binary.AppendUvarint(dst, uint64(len(v))), v...)
}

// dictKey returns the key of a value in the dictionary, adding the value in first-seen order
func dictKey[T ~string | ~[]byte](o *nativeOut, v T) uint64 {
	key := o.entries + o.dictDefaults()
	if found, ok := o.dict[string(v)]; ok {
		return found
	}
	o.dict[string(v)] = key
	o.entries++
	o.data = appendNativeString(o.data, v)
	return key
}

// dictDefaults is the count of entries that start the dictionary: NULL when the dictionary is
// nullable, and the type's default
func (o *nativeOut) dictDefaults() uint64 {
	return 1 + uint64(boolByte(o.dictNullable))
}

// box turns the column boxed, discarding what it wrote
func (o *nativeOut) box() {
	o.kind = outBoxed
	o.data, o.nulls, o.dict, o.keys = o.data[:0], o.nulls[:0], nil, o.keys[:0]
}

// boxedValue returns a row's value as clickhouse-go is given it: the time's text, the tag's, or the
// value itself
func (o *nativeOut) boxedValue(row outputRow, layout *nativeSeries, i int) any {
	switch o.role {
	case timeseries.RoleTimestamp:
		return o.time.format(row.epoch())
	case timeseries.RoleValue:
		return row.seg.Value(layout.index[i], row.i)
	}
	return layout.cells[i]
}

func (o *nativeOut) encode(w io.Writer, typ string, revision uint64) error {
	switch o.kind {
	case outBoxed:
		return server.EncodeNativeColumn(w, typ, o.values, revision)
	case outLowCardinality:
		return o.encodeLowCardinality(w)
	}
	if o.nullable {
		if _, err := w.Write(o.nulls); err != nil {
			return err
		}
	}
	_, err := w.Write(o.data)
	return err
}

// encodeLowCardinality writes the state prefix, the dictionary with its defaults first, and the keys
func (o *nativeOut) encodeLowCardinality(w io.Writer) error {
	size, flag := 1, uint64(0)
	switch n := o.entries; {
	case n >= math.MaxUint32:
		size, flag = 8, lowCardinalityKey64
	case n >= math.MaxUint16:
		size, flag = 4, lowCardinalityKey32
	case n >= math.MaxUint8:
		size, flag = 2, lowCardinalityKey16
	}
	b := binary.LittleEndian.AppendUint64(nil, lowCardinalityVersion)
	b = binary.LittleEndian.AppendUint64(b, lowCardinalityUpdateAll|flag)
	defaults := o.dictDefaults()
	b = binary.LittleEndian.AppendUint64(b, o.entries+defaults)
	// each default is an empty string
	b = append(b, make([]byte, defaults)...)
	if _, err := w.Write(b); err != nil {
		return err
	}
	if _, err := w.Write(o.data); err != nil {
		return err
	}
	o.size = size
	o.data = binary.LittleEndian.AppendUint64(o.data[:0], uint64(len(o.keys)))
	for _, k := range o.keys {
		o.appendWord(k)
	}
	_, err := w.Write(o.data)
	return err
}

// a series' cells that are the same on every row, boxed once; each value field's index in a point
// (-1 when the series has none); and each tag's dictionary key (noKey until it's found)
type nativeSeries struct {
	cells []any
	index []int
	keys  []uint64
}

const noKey = math.MaxUint64

func newNativeSeries(series *dataset.Series, fields timeseries.FieldDefinitions) *nativeSeries {
	ns := &nativeSeries{cells: make([]any, len(fields)), index: make([]int, len(fields)), keys: make([]uint64, len(fields))}
	for i, f := range fields {
		ns.index[i], ns.keys[i] = -1, noKey
		switch f.Role {
		case timeseries.RoleTimestamp:
		case timeseries.RoleTag:
			// a tag the series doesn't hold is NULL, which a column that isn't nullable writes as ""
			if value, ok := series.Header.Tags[f.Name]; ok || !nullableType(f.SDataType) {
				ns.cells[i] = value
			}
		case timeseries.RoleValue:
			// the last value field of a name is the one a lookup by name finds
			for j, vf := range series.Header.ValueFieldsList {
				if vf.Name == f.Name {
					ns.index[i] = j
				}
			}
		default:
			ns.cells[i] = f.DefaultValue
		}
	}
	return ns
}

// nullableType reports whether a type's values can be NULL: a Nullable, or a LowCardinality of one
func nullableType(typ string) bool {
	if inner, ok := cutWrapper(strings.TrimSpace(typ), prefixLowCardinality); ok {
		typ = inner
	}
	return strings.HasPrefix(typ, prefixNullable)
}

// how a time column's field formats a point's time: with a layout in the column's zone, which
// clickhouse-go reads it in, or as a count of units
type nativeTimeFormat struct {
	layout string
	unit   timeseries.FieldDataType
	loc    *time.Location
}

func newNativeTimeFormat(tfd timeseries.FieldDefinition) nativeTimeFormat {
	f := newOutField(&tfd, &FormatOptions{})
	switch f.class {
	case classDate:
		return nativeTimeFormat{layout: dateLayout, loc: time.UTC}
	case classDateTime:
		loc := time.UTC
		if f.zone != nil {
			loc = f.zone.Location()
		}
		if f.precision > 0 && f.precision <= maxPrecision {
			return nativeTimeFormat{layout: "2006-01-02 15:04:05." + strings.Repeat("0", f.precision), loc: loc}
		}
		return nativeTimeFormat{layout: timeconv.SQLDateTimeLayout, loc: loc}
	}
	// otherwise epoch seconds, or the field's finer unit, as a string
	return nativeTimeFormat{unit: tfd.DataType}
}

func (f nativeTimeFormat) format(ep epoch.Epoch) string {
	nanos := int64(ep)
	t := time.Unix(nanos/1e9, nanos%1e9).UTC()
	if f.loc != nil {
		t = t.In(f.loc)
	}
	if f.layout != "" {
		return t.Format(f.layout)
	}
	switch f.unit {
	case timeseries.DateTimeUnixMilli:
		return strconv.FormatInt(t.UnixMilli(), 10)
	case timeseries.DateTimeUnixMicro:
		return strconv.FormatInt(t.UnixMicro(), 10)
	case timeseries.DateTimeUnixNano:
		return strconv.FormatInt(t.UnixNano(), 10)
	default:
		return strconv.FormatInt(t.Unix(), 10)
	}
}
