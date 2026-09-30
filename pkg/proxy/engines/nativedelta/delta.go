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

package nativedelta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// Delta is a delta-tier result: the protocol's result metadata, replayed verbatim, and its rows as a
// DataSet whose series group them and whose points each hold one row.
type Delta struct {
	// Header is opaque to the engine, such as a PostgreSQL RowDescription or an Arrow schema
	Header []byte
	DS     *dataset.DataSet
}

// Rows returns the number of rows the Delta holds.
func (d *Delta) Rows() int {
	if d == nil || d.DS == nil {
		return 0
	}
	n := 0
	for _, r := range d.DS.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s != nil {
				n += s.PointCount()
			}
		}
	}
	return n
}

var (
	deltaCodecMagic = [4]byte{'T', 'N', 'D', 'R'}
	errDeltaCodec   = errors.New("invalid native delta rows")
)

const deltaCodecVersion byte = 1 // changes with the encoding, so entries of another version are misses

type deltaCodec struct{}

const (
	// the rows follow the header as a msgpack DataSet, or packed when every value is a row's bytes,
	// NULL or an integer, which decodes with no per-value parsing
	layoutDataSet byte = iota
	layoutPacked
)

func (c deltaCodec) Marshal(d *Delta) ([]byte, error) {
	return c.AppendMarshal(nil, d)
}

func (deltaCodec) AppendMarshal(out []byte, d *Delta) ([]byte, error) {
	if d == nil || d.DS == nil || len(d.Header) > math.MaxUint32 {
		return nil, errDeltaCodec
	}
	packedSize, packable := packedRowsSize(d.DS)
	var rows []byte
	if !packable {
		var err error
		if rows, err = dataset.MarshalDataSet(d.DS, nil, 0); err != nil {
			return nil, err
		}
		packedSize = len(rows)
	}
	out = slices.Grow(out, len(deltaCodecMagic)+1+4+len(d.Header)+1+packedSize)
	out = append(out, deltaCodecMagic[:]...)
	out = append(out, deltaCodecVersion)
	// #nosec G115 -- bounded by math.MaxUint32 above
	out = binary.BigEndian.AppendUint32(out, uint32(len(d.Header)))
	out = append(out, d.Header...)
	if !packable {
		return append(append(out, layoutDataSet), rows...), nil
	}
	return appendPackedRows(append(out, layoutPacked), d.DS)
}

func (deltaCodec) Unmarshal(data []byte) (*Delta, error) {
	const prefix = len(deltaCodecMagic) + 1 + 4
	if len(data) < prefix || !bytes.Equal(data[:len(deltaCodecMagic)], deltaCodecMagic[:]) ||
		data[len(deltaCodecMagic)] != deltaCodecVersion {
		return nil, errDeltaCodec
	}
	size := binary.BigEndian.Uint32(data[prefix-4 : prefix])
	// #nosec G115 -- a length is never negative
	if uint64(size) >= uint64(len(data)-prefix) {
		return nil, errDeltaCodec
	}
	// the header and the rows' values refer to data, which the tier hands over as the codec's own
	body := data[prefix:]
	header, layout, rows := body[:size:size], body[size], body[size+1:]
	switch layout {
	case layoutPacked:
		ds, err := readPackedRows(rows)
		if err != nil {
			return nil, err
		}
		return &Delta{Header: header, DS: ds}, nil
	case layoutDataSet:
		ts, err := dataset.UnmarshalDataSet(rows, nil)
		if err != nil {
			return nil, err
		}
		if ds, ok := ts.(*dataset.DataSet); ok {
			return &Delta{Header: header, DS: ds}, nil
		}
	}
	return nil, errDeltaCodec
}

func (deltaCodec) Size(d *Delta) int {
	if d == nil {
		return 0
	}
	size := len(d.Header)
	if d.DS != nil {
		size += int(d.DS.Size())
	}
	return size
}
