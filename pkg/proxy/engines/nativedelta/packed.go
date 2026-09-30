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
	"encoding/binary"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const (
	packedNull byte = iota
	packedBytes
	packedInt
)

// the fewest integers a decode's chunk of them holds
const minIntChunk = 64

func packedRowsSize(ds *dataset.DataSet) (int, bool) {
	// whether every value packs, as the delta tier's row bytes and sequences do, and the bytes it needs
	size := 16 + 2*binary.MaxVarintLen64*len(ds.ExtentList)
	for _, r := range ds.Results {
		if r == nil {
			return 0, false
		}
		size += 3*binary.MaxVarintLen64 + len(r.Name) + len(r.Error)
		for _, s := range r.SeriesList {
			if s == nil {
				return 0, false
			}
			size += s.Header.Msgsize() + 2*binary.MaxVarintLen64
			width := -1
			pts := s.FlatPoints()
			for i := range pts {
				values := pts[i].Values
				if width < 0 {
					width = len(values)
				} else if len(values) != width {
					return 0, false
				}
				size += binary.MaxVarintLen64
				for _, v := range values {
					switch v := v.(type) {
					case nil:
						size++
					case []byte:
						size += 1 + binary.MaxVarintLen64 + len(v)
					case *[]byte:
						if v != nil {
							size += 1 + binary.MaxVarintLen64 + len(*v)
						} else {
							size++
						}
					case int64, int:
						size += 1 + binary.MaxVarintLen64
					case *int64:
						size += 1 + binary.MaxVarintLen64
					default:
						return 0, false
					}
				}
			}
		}
	}
	return size, true
}

func appendPackedRows(out []byte, ds *dataset.DataSet) ([]byte, error) {
	out = binary.AppendUvarint(out, uint64(len(ds.ExtentList)))
	for _, e := range ds.ExtentList {
		out = binary.AppendVarint(binary.AppendVarint(out, e.Start.UnixNano()), e.End.UnixNano())
	}
	out = binary.AppendUvarint(out, uint64(len(ds.Results)))
	for _, r := range ds.Results {
		out = binary.AppendVarint(out, int64(r.StatementID))
		out = appendPackedString(appendPackedString(out, r.Name), r.Error)
		out = binary.AppendUvarint(out, uint64(len(r.SeriesList)))
		for _, s := range r.SeriesList {
			var err error
			if out, err = s.Header.MarshalMsg(out); err != nil {
				return nil, err
			}
			width := 0
			pts := s.FlatPoints()
			if len(pts) > 0 {
				width = len(pts[0].Values)
			}
			out = binary.AppendUvarint(binary.AppendUvarint(out, uint64(len(pts))), uint64(width))
			for i := range pts {
				out = binary.AppendVarint(out, int64(pts[i].Epoch))
				// the switch is inline, as a call per value costs the encode a tenth of its time
				for _, v := range pts[i].Values {
					switch t := v.(type) {
					case *[]byte:
						if t == nil {
							out = append(out, packedNull)
							continue
						}
						out = append(binary.AppendUvarint(append(out, packedBytes), uint64(len(*t))), *t...)
					case []byte:
						out = append(binary.AppendUvarint(append(out, packedBytes), uint64(len(t))), t...)
					case *int64:
						if t == nil {
							out = append(out, packedNull)
							continue
						}
						out = binary.AppendVarint(append(out, packedInt), *t)
					case int64:
						out = binary.AppendVarint(append(out, packedInt), t)
					case int:
						out = binary.AppendVarint(append(out, packedInt), int64(t))
					default:
						out = append(out, packedNull)
					}
				}
			}
		}
	}
	return out, nil
}

func appendPackedString(out []byte, s string) []byte {
	return append(binary.AppendUvarint(out, uint64(len(s))), s...)
}

type packedReader struct {
	data []byte
	err  bool
}

func (p *packedReader) uvarint() uint64 {
	v, n := binary.Uvarint(p.data)
	if n <= 0 {
		p.err, p.data = true, nil
		return 0
	}
	p.data = p.data[n:]
	return v
}

func (p *packedReader) varint() int64 {
	v, n := binary.Varint(p.data)
	if n <= 0 {
		p.err, p.data = true, nil
		return 0
	}
	p.data = p.data[n:]
	return v
}

func (p *packedReader) bytes() []byte {
	// the bytes refer to the reader's data, which the caller owns
	n := p.uvarint()
	if p.err || n > uint64(len(p.data)) {
		p.err, p.data = true, nil
		return nil
	}
	out := p.data[:n:n]
	p.data = p.data[n:]
	return out
}

func (p *packedReader) count(minimum int) int {
	// a count of items taking at least minimum bytes each, so corrupt data can't size an allocation
	n := p.uvarint()
	if p.err || n > uint64(len(p.data)/max(minimum, 1)) {
		p.err, p.data = true, nil
		return 0
	}
	return int(n) // #nosec G115 -- bounded by len(p.data) above
}

func readPackedRows(data []byte) (*dataset.DataSet, error) {
	p := &packedReader{data: data}
	ds := &dataset.DataSet{}
	if n := p.count(2); n > 0 {
		ds.ExtentList = make(timeseries.ExtentList, n)
		for i := range ds.ExtentList {
			ds.ExtentList[i] = timeseries.Extent{Start: time.Unix(0, p.varint()), End: time.Unix(0, p.varint())}
		}
	}
	results := p.count(4)
	ds.Results = make(dataset.Results, 0, results)
	for range results {
		r := &dataset.Result{StatementID: int(p.varint()), Name: string(p.bytes()), Error: string(p.bytes())}
		series := p.count(1)
		r.SeriesList = make(dataset.SeriesList, 0, series)
		for range series {
			s, err := readPackedSeries(p)
			if err != nil {
				return nil, err
			}
			r.SeriesList = append(r.SeriesList, s)
		}
		ds.Results = append(ds.Results, r)
	}
	if p.err || len(p.data) != 0 {
		return nil, errDeltaCodec
	}
	return ds, nil
}

func readPackedSeries(p *packedReader) (*dataset.Series, error) {
	s := &dataset.Series{}
	rest, err := s.Header.UnmarshalMsg(p.data)
	if err != nil {
		return nil, errDeltaCodec
	}
	p.data = rest
	n, width := p.count(1), p.count(0)
	if p.err || (n > 0 && width > len(p.data)/n) {
		return nil, errDeltaCodec
	}
	// one allocation holds every point's values, another the headers of their bytes and a third
	// their integers, which box as pointers without an allocation each
	s.Points = make(dataset.Points, n)
	values := make([]any, n*width)
	blobs := make([][]byte, 0, n*width)
	var ints []int64
	for i := range s.Points {
		at := epoch.Epoch(p.varint())
		row := values[i*width : (i+1)*width : (i+1)*width]
		for j := range row {
			if len(p.data) == 0 {
				return nil, errDeltaCodec
			}
			kind := p.data[0]
			p.data = p.data[1:]
			switch kind {
			case packedBytes:
				blobs = append(blobs, p.bytes())
				row[j] = &blobs[len(blobs)-1]
			case packedInt:
				if len(ints) == cap(ints) {
					// a new chunk, as those already pointed to must not move
					ints = make([]int64, 0, max(n, minIntChunk))
				}
				ints = append(ints, p.varint())
				row[j] = &ints[len(ints)-1]
			case packedNull:
			default:
				return nil, errDeltaCodec
			}
		}
		size := dataset.PointSize(row)
		s.Points[i] = dataset.Point{Epoch: at, Size: size, Values: row}
		s.PointSize += int64(size)
	}
	if p.err {
		return nil, errDeltaCodec
	}
	return s, nil
}
