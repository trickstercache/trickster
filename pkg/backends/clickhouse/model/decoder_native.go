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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// nullToken is the text of a NULL; it matches ClickHouse's own TSV null literal so both wire
// formats produce identical datasets.
const nullToken = "\\N"

const (
	// the block info fields that precede a block's columns when the client sends its protocol version
	blockInfoEnd      = 0
	blockInfoOverflow = 1
	blockInfoBucket   = 2
	bucketNumLen      = 4
	// the smallest buffer read into, and the least a buffer grows before a block that hadn't all
	// arrived is read again
	nativeReadSize = 32 << 10
	// the largest input buffer kept for the next decode
	maxPooledInput = 16 << 20
)

var (
	errUnsupportedSerialization = errors.New("custom column serialization is not supported")
	errUnsupportedColumnType    = errors.New("unsupported native column type")
	// the input ends inside a value; a Write waits for more of it, and Finish fails
	errShort = errors.New("native: short input")
	// a varint longer than a uint64
	errVarintOverflow = errors.New("native: varint overflows a 64-bit integer")
)

var (
	unmarshalNative       = stream.BytesUnmarshaler(newNativeDecoder)
	unmarshalNativeReader = stream.ReaderUnmarshaler(newNativeDecoder)
)

// UnmarshalTimeseriesNative decodes a ClickHouse Native response, blocks each holding its row count
// and each column's name, type and values, into a Timeseries (DataSet).
func UnmarshalTimeseriesNative(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return unmarshalNative(data, trq)
}

// UnmarshalTimeseriesNativeReader decodes a ClickHouse Native binary response
// from an io.Reader into a Timeseries (DataSet), a block at a time.
func UnmarshalTimeseriesNativeReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return unmarshalNativeReader(reader, trq)
}

// nativeDecoder reads the Native format's blocks as they arrive: a block is read once all of it
// is buffered, and each of its rows is typed as the same row of TSV text would be
type nativeDecoder struct {
	d *decoder
	*nativeBuffers
	// the buffered length at which a block that hadn't all arrived is read again
	retryAt int
	// the blocks carry block info, as the first block to have it showed
	info bool
	// a block's header didn't read, or it had no rows, so the input after it is ignored
	stopped bool
	// the input ended before any block, or its first block had no rows
	empty  bool
	blocks int
	err    error
	done   bool
}

// nativeBuffers are a decode's input and its block's columns, which the DataSet never references,
// so they're reused by the next decode
type nativeBuffers struct {
	buf  []byte
	cols []nativeColumn
}

var nativeBufferPool = sync.Pool{New: func() any { return &nativeBuffers{} }}

func newNativeDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	d, err := newDecoder(trq, nil)
	if err != nil {
		return nil, err
	}
	nb := nativeBufferPool.Get().(*nativeBuffers)
	nb.buf = nb.buf[:0]
	return &nativeDecoder{d: d, nativeBuffers: nb}, nil
}

// grow makes room for n more bytes of input, doubling the buffer as it must grow
func (n *nativeDecoder) grow(size int) {
	if cap(n.buf)-len(n.buf) >= size {
		return
	}
	buf := make([]byte, len(n.buf), max(2*cap(n.buf), len(n.buf)+size))
	copy(buf, n.buf)
	n.buf = buf
}

func (n *nativeDecoder) Write(p []byte) (int, error) {
	if err := n.check(); err != nil {
		return 0, err
	}
	if !n.stopped {
		n.grow(len(p))
		n.buf = append(n.buf, p...)
		if err := n.decode(false); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// ReadFrom reads r to its end into the decoder's buffer, reading each block once it has arrived
func (n *nativeDecoder) ReadFrom(r io.Reader) (int64, error) {
	if err := n.check(); err != nil {
		return 0, err
	}
	var total int64
	for {
		n.grow(nativeReadSize)
		m, err := r.Read(n.buf[len(n.buf):cap(n.buf)])
		total += int64(m)
		if m > 0 && !n.stopped {
			n.buf = n.buf[:len(n.buf)+m]
			if derr := n.decode(false); derr != nil {
				return total, derr
			}
		}
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			n.err = err
			return total, err
		}
	}
}

func (n *nativeDecoder) Finish() (timeseries.Timeseries, error) {
	if n.done {
		return nil, stream.ErrFinished
	}
	n.done = true
	if n.err != nil {
		return nil, n.err
	}
	err := n.decode(true)
	if cap(n.buf) <= maxPooledInput {
		nativeBufferPool.Put(n.nativeBuffers)
	}
	n.nativeBuffers = nil
	if err != nil {
		return nil, err
	}
	if n.blocks == 0 {
		if !n.empty {
			return nil, timeseries.ErrInvalidBody
		}
		// no rows, as an empty body or a block without any holds, is no results
		return &dataset.DataSet{
			TimeRangeQuery: n.d.trq, ExtentList: timeseries.ExtentList{n.d.trq.Extent},
			Results: dataset.Results{},
		}, nil
	}
	return n.d.finish()
}

func (n *nativeDecoder) check() error {
	if n.done {
		return stream.ErrFinished
	}
	return n.err
}

// decode reads each whole block in the buffer. A block that hasn't all arrived is read again once
// the buffer doubles, or when the input ends (final), when its end is read as the end of the input.
func (n *nativeDecoder) decode(final bool) error {
	for !n.stopped && (final || len(n.buf) >= n.retryAt) {
		cur := nativeCursor{b: n.buf, final: final}
		err := n.block(&cur)
		if errors.Is(err, errShort) && !final {
			n.retryAt = 2 * max(len(n.buf), nativeReadSize)
			return nil
		}
		if err != nil {
			n.err = err
			return err
		}
		n.buf = n.buf[:copy(n.buf, n.buf[cur.off:])]
		n.retryAt = 0
	}
	return nil
}

// block reads one block and adds its rows. A header that doesn't read ends the input, as does an
// empty block; a column that doesn't read fails the decode.
func (n *nativeDecoder) block(cur *nativeCursor) error {
	numCols, numRows, err := n.blockHeader(cur)
	if errors.Is(err, errShort) && !cur.final {
		return err
	}
	if err != nil || numCols == 0 || numRows == 0 {
		n.empty = n.empty || (err == nil && n.blocks == 0)
		n.stopped = true
		return nil
	}
	// a column's name and type take a byte each, and its values a byte a row, so more of either
	// than the bytes left can't have arrived yet
	if numCols > cur.left() || numRows > cur.left() {
		return cur.short()
	}
	rows := int(numRows) //nolint:gosec // bounded by the buffered bytes
	var names, types []string
	if n.blocks == 0 {
		names, types = make([]string, 0, numCols), make([]string, 0, numCols)
	}
	for c := range int(numCols) { //nolint:gosec // bounded by the buffered bytes
		name, err := cur.str()
		if err != nil {
			return fmt.Errorf("native: column %d name: %w", c, cur.eof(err))
		}
		typ, err := cur.str()
		if err != nil {
			return fmt.Errorf("native: column %d type: %w", c, cur.eof(err))
		}
		if n.info {
			flag, err := cur.byte()
			if err != nil {
				return fmt.Errorf("native: column %d custom serialization flag: %w", c, cur.eof(err))
			}
			if flag != 0 && !strings.HasPrefix(strings.TrimSpace(string(typ)), "LowCardinality(") {
				return fmt.Errorf("native: column %q: %w", name, errUnsupportedSerialization)
			}
		}
		if c == len(n.cols) {
			n.cols = append(n.cols, nativeColumn{})
		}
		if err := n.cols[c].readColumn(cur, string(typ), rows); err != nil {
			return fmt.Errorf("native: column %q: %w", name, cur.eof(err))
		}
		if n.blocks == 0 {
			names, types = append(names, string(name)), append(types, string(typ))
		}
	}
	if n.blocks == 0 {
		n.d.header = [dataStartRow][]string{names, types}
		n.d.width = len(names)
		if err := n.d.layout(); err != nil {
			return err
		}
	}
	n.blocks++
	// a block of another width than the first can't be laid out by its fields, so its rows are skipped
	if int(numCols) != n.d.width { //nolint:gosec // bounded by the buffered bytes
		return nil
	}
	for r := range rows {
		if err := n.d.nativeRow(n.cols, r); err != nil {
			return err
		}
	}
	return nil
}

// blockHeader reads the block info, when the block has it, and the block's column and row counts
func (n *nativeDecoder) blockHeader(cur *nativeCursor) (numCols, numRows uint64, err error) {
	if cur.remaining() == 0 {
		n.empty = n.blocks == 0 && cur.final
		return 0, 0, errShort
	}
	if cur.b[cur.off] == blockInfoOverflow {
		n.info = true
		if err := skipBlockInfo(cur); err != nil {
			return 0, 0, err
		}
	}
	if numCols, err = cur.uvarint(); err != nil {
		return 0, 0, err
	}
	numRows, err = cur.uvarint()
	return numCols, numRows, err
}

func skipBlockInfo(cur *nativeCursor) error {
	for {
		field, err := cur.uvarint()
		if err != nil {
			return err
		}
		switch field {
		case blockInfoEnd:
			return nil
		case blockInfoOverflow:
			_, err = cur.byte()
		case blockInfoBucket:
			_, err = cur.fixed(bucketNumLen)
		default:
			return fmt.Errorf("unknown block info field: %d", field)
		}
		if err != nil {
			return err
		}
	}
}

// nativeRow adds row r of a block's columns; a row whose time doesn't read is dropped
func (d *decoder) nativeRow(cols []nativeColumn, r int) error {
	e, err := d.nativeTime(&cols[d.timeCol], r)
	if err != nil {
		d.drop(err)
		return nil
	}
	rb := d.b.Row()
	rb.SetEpoch(e)
	// a NULL tag is left out of the series' tags
	for i, c := range d.tagCols {
		if !cols[c].nullAt(r) {
			rb.SetTag(i, cols[c].text(&d.scratch, r))
		}
	}
	for i, c := range d.valCols {
		cols[c].add(rb, d.fields.Values[i].DataType, r, &d.scratch)
	}
	return rb.Commit()
}

// nativeTime returns a row's time, as the time's text reads, from the column's values when their
// type and the time field's give the same time
func (d *decoder) nativeTime(c *nativeColumn, r int) (epoch.Epoch, error) {
	for c.kind == colDict && !c.null(r) {
		c, r = c.dict, int(c.words[r]) //nolint:gosec // an index checked against the dictionary
	}
	if !c.null(r) {
		switch dt, w := d.fields.Timestamp.DataType, c.words; {
		case c.kind == colDateTime && dt == timeseries.DateTimeSQL:
			return epoch.Epoch(int64(w[r]) * int64(epoch.BillionNS)), nil //nolint:gosec // a uint32
		case c.kind == colDate && dt == timeseries.DateSQL:
			return epoch.Epoch(int64(w[r]) * secondsPerDay * int64(epoch.BillionNS)), nil //nolint:gosec // a uint16
		case c.kind == colDateTime64 && dt == timeseries.DateTimeSQL:
			if e, ok := dateTime64Epoch(int64(w[r]), c.precision); ok { //nolint:gosec // the wire's int64
				return e, nil
			}
		}
	}
	return d.textTime(c.text(&d.scratch, r))
}

// dateTime64Epoch returns the time of a DateTime64 value, wrapping as time.Time.UnixNano does, when
// its text has a four-digit year, which the text's parser requires
func dateTime64Epoch(v int64, precision int) (epoch.Epoch, bool) {
	scale := pow10[precision]
	secs := v / scale
	if v%scale < 0 {
		secs--
	}
	if secs < minTextSeconds || secs >= maxTextSeconds {
		return 0, false
	}
	return epoch.Epoch(v * pow10[maxPrecision-precision]), true
}

const (
	secondsPerDay = 86400
	// DateTime64's finest precision, in nanoseconds
	maxPrecision = 9
	// the seconds from the epoch to 0000-01-01 and 10000-01-01, between which a year takes four digits
	minTextSeconds = -62167219200
	maxTextSeconds = 253402300800
)

var pow10 = [maxPrecision + 1]int64{1, 10, 100, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9}

// nativeCursor reads the values of a buffered block
type nativeCursor struct {
	b     []byte
	off   int
	final bool
}

func (c *nativeCursor) remaining() int {
	return len(c.b) - c.off
}

// left is remaining as the unsigned sizes the wire declares
func (c *nativeCursor) left() uint64 {
	return uint64(c.remaining()) //nolint:gosec // never negative
}

// short returns the error of a value that hasn't all arrived, which is the input's end when final
func (c *nativeCursor) short() error {
	return c.eof(errShort)
}

// eof returns err as the end of the input when it ran short
func (c *nativeCursor) eof(err error) error {
	if errors.Is(err, errShort) && c.final {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (c *nativeCursor) uvarint() (uint64, error) {
	v, n := binary.Uvarint(c.b[c.off:])
	switch {
	case n == 0:
		return 0, errShort
	case n < 0:
		return 0, errVarintOverflow
	}
	c.off += n
	return v, nil
}

func (c *nativeCursor) byte() (byte, error) {
	if c.off >= len(c.b) {
		return 0, errShort
	}
	c.off++
	return c.b[c.off-1], nil
}

// fixed returns the next n bytes, which alias the buffer
func (c *nativeCursor) fixed(n int) ([]byte, error) {
	if n < 0 || n > c.remaining() {
		return nil, errShort
	}
	c.off += n
	return c.b[c.off-n : c.off], nil
}

// str returns the next length-prefixed string, which aliases the buffer
func (c *nativeCursor) str() ([]byte, error) {
	n, err := c.uvarint()
	if err != nil {
		return nil, err
	}
	if n > c.left() {
		return nil, errShort
	}
	return c.fixed(int(n)) //nolint:gosec // bounded by the buffered bytes
}

// words returns the next n little-endian values of size bytes
func (c *nativeCursor) words(n, size int) ([]byte, error) {
	if n > c.remaining()/size {
		return nil, errShort
	}
	return c.fixed(n * size)
}
