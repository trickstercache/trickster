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
package pgwire

import (
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	msgRowDescription  = 'T'
	msgCommandComplete = 'C'
	msgEmptyQuery      = 'I'
	msgNotice          = 'N'
	msgNotification    = 'A'

	selectTagPrefix = "SELECT "
	columnCountLen  = 2
	columnSizeLen   = 4
	nullColumnSize  = -1

	resultCodecVersion byte = 1
	resultFlagTimes    byte = 1
)

var (
	errResultCodec = errors.New("invalid cached postgres result")
	errResultRow   = errors.New("invalid postgres result row")
)

// Result is one buffered query result, as the object tier and the relay keep it. Rows stay exactly
// as the origin sent them.
type Result struct {
	// RowDescription is the origin's message body, replayed verbatim.
	RowDescription []byte
	// Tag is the origin's CommandComplete tag, used as is.
	Tag string

	data []byte
	ends []uint32
}

// Rows returns the number of rows held.
func (r *Result) Rows() int { return len(r.ends) }

func (r *Result) row(i int) []byte {
	start := uint32(0)
	if i > 0 {
		start = r.ends[i-1]
	}
	return r.data[start:r.ends[i]]
}

func rowColumn(body []byte, index int) ([]byte, error) {
	// returns the text of one column of a DataRow body, or nil for NULL.
	if len(body) < columnCountLen || int(binary.BigEndian.Uint16(body)) <= index {
		return nil, errResultRow
	}
	body = body[columnCountLen:]
	for column := 0; ; column++ {
		if len(body) < columnSizeLen {
			return nil, errResultRow
		}
		size := int32(binary.BigEndian.Uint32(body)) // #nosec G115 -- the wire value is a signed length
		body = body[columnSizeLen:]
		if size == nullColumnSize {
			if column == index {
				return nil, nil
			}
			continue
		}
		if size < 0 || int(size) > len(body) {
			return nil, errResultRow
		}
		if column == index {
			return body[:size], nil
		}
		body = body[size:]
	}
}

func (r *Result) encode() []byte {
	// renders the whole response to one Query
	out := make([]byte, 0, len(r.RowDescription)+len(r.data)+frameHeaderLen*(r.Rows()+3)+len(r.Tag)+16)
	if r.RowDescription != nil {
		out = appendFrame(out, msgRowDescription, r.RowDescription)
	}
	for row := range r.ends {
		out = appendFrame(out, msgDataRow, r.row(row))
	}
	tag := r.Tag
	if tag == "" {
		tag = selectTagPrefix + strconv.Itoa(r.Rows())
	}
	out = appendFrame(out, msgCommandComplete, append([]byte(tag), 0))
	return appendFrame(out, msgReadyForQuery, []byte{txStatusIdle})
}

type resultCodec struct{}

func (resultCodec) Size(r *Result) int {
	if r == nil {
		return 0
	}
	return len(r.RowDescription) + len(r.Tag) + len(r.data) + 4*len(r.ends)
}

func (resultCodec) Marshal(r *Result) ([]byte, error) {
	if r == nil {
		return nil, errResultCodec
	}
	out := make([]byte, 0, len(r.RowDescription)+len(r.Tag)+len(r.data)+3*len(r.ends)+32)
	out = append(out, resultCodecVersion, 0)
	out = appendBytes(out, r.RowDescription)
	out = appendBytes(out, []byte(r.Tag))
	out = binary.AppendUvarint(out, uint64(len(r.ends)))
	previousEnd := uint32(0)
	for _, end := range r.ends {
		out = binary.AppendUvarint(out, uint64(end-previousEnd))
		previousEnd = end
	}
	return append(out, r.data...), nil
}

func (resultCodec) Unmarshal(data []byte) (*Result, error) {
	// a timed entry was written for delta rows, which the delta tier now keeps itself
	if len(data) < 2 || data[0] != resultCodecVersion || data[1]&resultFlagTimes != 0 {
		return nil, errResultCodec
	}
	data = data[2:]
	r := &Result{}
	var (
		tag []byte
		ok  bool
	)
	if r.RowDescription, data, ok = readBytes(data); !ok {
		return nil, errResultCodec
	}
	if tag, data, ok = readBytes(data); !ok {
		return nil, errResultCodec
	}
	r.Tag = string(tag)
	if len(r.RowDescription) == 0 {
		r.RowDescription = nil
	}
	rows, n := binary.Uvarint(data)
	if n <= 0 || rows > uint64(len(data)) {
		return nil, errResultCodec
	}
	data = data[n:]
	r.ends = make([]uint32, 0, rows)
	end := uint64(0)
	for range rows {
		size, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, errResultCodec
		}
		data = data[n:]
		if end += size; size > math.MaxUint32 || end > math.MaxUint32 {
			return nil, errResultCodec
		}
		r.ends = append(r.ends, uint32(end))
	}
	if end != uint64(len(data)) {
		return nil, errResultCodec
	}
	r.data = slices.Clone(data)
	return r, nil
}

func appendBytes(out, value []byte) []byte {
	return append(binary.AppendUvarint(out, uint64(len(value))), value...)
}

func readBytes(data []byte) (value, rest []byte, ok bool) {
	size, n := binary.Uvarint(data)
	if n <= 0 || size > math.MaxInt32 || int(size) > len(data)-n {
		return nil, nil, false
	}
	end := n + int(size)
	return slices.Clone(data[n:end]), data[end:], true
}

func bucketTime(body []byte, column int, decoder *timeAxisDecoder, step, phase time.Duration) (int64, error) {
	// decodes one row's bucket on the time axis and checks it lies on the plan's grid.
	text, err := rowColumn(body, column)
	if err != nil {
		return 0, err
	}
	if text == nil {
		return 0, errTimeAxis
	}
	value, err := decoder.decode(text)
	if err != nil {
		return 0, err
	}
	if !timeseries.OnGrid(value, step, phase) {
		// a value off the grid means the origin bucketed differently than planned
		return 0, errTimeAxis
	}
	return value.UnixNano(), nil
}
