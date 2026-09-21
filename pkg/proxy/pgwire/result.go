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
	"cmp"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
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

// Result is one buffered query result. Rows stay exactly as the origin sent
// them; a delta result also carries each row's decoded bucket time.
type Result struct {
	// RowDescription is the origin's message body, replayed verbatim.
	RowDescription []byte
	// Tag is the origin's CommandComplete tag, used as is for object results.
	Tag string

	data  []byte
	ends  []uint32
	times []int64
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

func (r *Result) appendRow(body []byte, bucket int64, timed bool) {
	r.data = append(r.data, body...)
	// #nosec G115 -- the fetch caps a result far below 4 GiB
	r.ends = append(r.ends, uint32(len(r.data)))
	if timed {
		r.times = append(r.times, bucket)
	}
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

func (r *Result) sortByTime() {
	// puts a delta result's rows in ascending bucket order, keeping the
	// origin's order inside each bucket so its collation never has to be reproduced.
	if slices.IsSorted(r.times) {
		return
	}
	order := make([]int, len(r.times))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(r.times[a], r.times[b]) })
	sorted := &Result{
		data: make([]byte, 0, len(r.data)), ends: make([]uint32, 0, len(r.ends)),
		times: make([]int64, 0, len(r.times)),
	}
	for _, i := range order {
		sorted.appendRow(r.row(i), r.times[i], true)
	}
	r.data, r.ends, r.times = sorted.data, sorted.ends, sorted.times
}

func (r *Result) slice(from, to int) *Result {
	// returns rows [from, to) as a view that shares the receiver's memory.
	out := &Result{RowDescription: r.RowDescription, Tag: r.Tag}
	if from >= to {
		if r.times != nil {
			out.times = []int64{}
		}
		return out
	}
	start := uint32(0)
	if from > 0 {
		start = r.ends[from-1]
	}
	out.data = r.data[start:r.ends[to-1]]
	out.ends = make([]uint32, to-from)
	for i := range out.ends {
		out.ends[i] = r.ends[from+i] - start
	}
	out.times = r.times[from:to]
	return out
}

func (r *Result) crop(extent timeseries.Extent) *Result {
	// keeps the buckets inside the inclusive extent.
	lower, upper := extent.Start.UnixNano(), extent.End.UnixNano()
	from := sort.Search(len(r.times), func(i int) bool { return r.times[i] >= lower })
	to := sort.Search(len(r.times), func(i int) bool { return r.times[i] > upper })
	return r.slice(from, to)
}

func (r *Result) retain(limit int) (*Result, int64, bool) {
	// keeps the newest buckets, at most limit of them, and reports the
	// first bucket time kept.
	if limit <= 0 || len(r.times) == 0 {
		return r, 0, false
	}
	buckets, from := 0, 0
	for i, v := range slices.Backward(r.times) {
		if i == len(r.times)-1 || v != r.times[i+1] {
			buckets++
			if buckets > limit {
				from = i + 1
				break
			}
		}
	}
	if from == 0 {
		return r, 0, false
	}
	return r.slice(from, len(r.times)), r.times[from], true
}

type bucketSegment struct {
	bucket   int64
	part     int
	from, to int
}

func mergeResults(parts []*Result) (*Result, error) {
	// combines delta parts into one ascending result. When two parts
	// hold the same bucket, the later part wins, since it was fetched more recently.
	var segments []bucketSegment
	merged := &Result{times: []int64{}}
	size, rows := 0, 0
	for index, part := range parts {
		if part == nil || part.times == nil && part.Rows() > 0 {
			return nil, errResultRow
		}
		if merged.RowDescription == nil || part.Rows() > 0 {
			merged.RowDescription = part.RowDescription
		}
		size += len(part.data)
		rows += part.Rows()
		for from := 0; from < len(part.times); {
			to := from + 1
			for to < len(part.times) && part.times[to] == part.times[from] {
				to++
			}
			segments = append(segments, bucketSegment{bucket: part.times[from], part: index, from: from, to: to})
			from = to
		}
	}
	slices.SortStableFunc(segments, func(a, b bucketSegment) int {
		return cmp.Or(cmp.Compare(a.bucket, b.bucket), cmp.Compare(a.part, b.part))
	})
	merged.data, merged.ends, merged.times = make([]byte, 0, size), make([]uint32, 0, rows), make([]int64, 0, rows)
	for i, segment := range segments {
		if i+1 < len(segments) && segments[i+1].bucket == segment.bucket {
			continue
		}
		for row := segment.from; row < segment.to; row++ {
			merged.appendRow(parts[segment.part].row(row), segment.bucket, true)
		}
	}
	return merged, nil
}

func (r *Result) encode(descending bool) []byte {
	// renders the whole response to one Query. descending emits a delta result's
	// buckets newest first, leaving each bucket's rows in the origin's order.
	out := make([]byte, 0, len(r.RowDescription)+len(r.data)+frameHeaderLen*(r.Rows()+3)+len(r.Tag)+16)
	if r.RowDescription != nil {
		out = appendFrame(out, msgRowDescription, r.RowDescription)
	}
	if descending && r.times != nil {
		for to := len(r.times); to > 0; {
			from := to - 1
			for from > 0 && r.times[from-1] == r.times[to-1] {
				from--
			}
			for row := from; row < to; row++ {
				out = appendFrame(out, msgDataRow, r.row(row))
			}
			to = from
		}
	} else {
		for row := range r.ends {
			out = appendFrame(out, msgDataRow, r.row(row))
		}
	}
	tag := r.Tag
	if r.times != nil || tag == "" {
		// a merged or cropped result has its own row count
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
	return len(r.RowDescription) + len(r.Tag) + len(r.data) + 4*len(r.ends) + 8*len(r.times)
}

func (resultCodec) Marshal(r *Result) ([]byte, error) {
	if r == nil {
		return nil, errResultCodec
	}
	out := make([]byte, 0, len(r.RowDescription)+len(r.Tag)+len(r.data)+3*len(r.ends)+3*len(r.times)+32)
	flags := byte(0)
	if r.times != nil {
		flags = resultFlagTimes
	}
	out = append(out, resultCodecVersion, flags)
	out = appendBytes(out, r.RowDescription)
	out = appendBytes(out, []byte(r.Tag))
	out = binary.AppendUvarint(out, uint64(len(r.ends)))
	previousEnd, previousTime := uint32(0), int64(0)
	for i, end := range r.ends {
		out = binary.AppendUvarint(out, uint64(end-previousEnd))
		previousEnd = end
		if r.times != nil {
			out = binary.AppendVarint(out, r.times[i]-previousTime)
			previousTime = r.times[i]
		}
	}
	return append(out, r.data...), nil
}

func (resultCodec) Unmarshal(data []byte) (*Result, error) {
	if len(data) < 2 || data[0] != resultCodecVersion {
		return nil, errResultCodec
	}
	timed := data[1]&resultFlagTimes != 0
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
	if timed {
		r.times = make([]int64, 0, rows)
	}
	end, bucket := uint64(0), int64(0)
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
		if timed {
			delta, n := binary.Varint(data)
			if n <= 0 {
				return nil, errResultCodec
			}
			data = data[n:]
			bucket += delta
			r.times = append(r.times, bucket)
		}
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
	if !sqlanalyzer.AlignedToBucket(value, step, phase) {
		// a value off the grid means the origin bucketed differently than planned
		return 0, errTimeAxis
	}
	return value.UnixNano(), nil
}
