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

package dataset

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/tinylib/msgp/msgp"
)

// A Series encodes as a msgpack map of its header and its rows, the rows a bin holding the segment
// encoding, whose words are aligned from the start of the buffer the Series is appended to.

const (
	seriesKeyHeader   = "header"
	seriesKeySegments = "segments"
	// the key a Series encoded its rows under before they were held by column
	seriesKeyLegacyPoints = "points"
	bin32Marker           = 0xc6
	bin32HeaderLen        = 5
)

// ErrLegacySeries indicates an encoded Series in the layout from before rows were held by column,
// which a cache refetches rather than reads.
var ErrLegacySeries = errors.New("series encoded in the legacy point layout")

// MarshalMsg implements msgp.Marshaler.
func (z *Series) MarshalMsg(b []byte) ([]byte, error) {
	o := msgp.AppendMapHeader(b, 2)
	o = msgp.AppendString(o, seriesKeyHeader)
	o, err := z.Header.MarshalMsg(o)
	if err != nil {
		return b, msgp.WrapError(err, "Header")
	}
	o = msgp.AppendString(o, seriesKeySegments)
	start := len(o)
	o = append(o, bin32Marker, 0, 0, 0, 0)
	if o, err = AppendSegments(o, 0, z.segs); err != nil {
		return b, msgp.WrapError(err, "Segments")
	}
	// #nosec G115 -- the segments are bounded by the 4 GiB a column's data can hold
	binary.BigEndian.PutUint32(o[start+1:], uint32(len(o)-start-bin32HeaderLen))
	return o, nil
}

// UnmarshalMsg implements msgp.Unmarshaler. The rows may share bts's memory, which must not change.
func (z *Series) UnmarshalMsg(bts []byte) ([]byte, error) {
	n, bts, err := msgp.ReadMapHeaderBytes(bts)
	if err != nil {
		return bts, err
	}
	z.segs = nil
	for range n {
		var key []byte
		if key, bts, err = msgp.ReadMapKeyZC(bts); err != nil {
			return bts, err
		}
		switch msgp.UnsafeString(key) {
		case seriesKeyHeader:
			if bts, err = z.Header.UnmarshalMsg(bts); err != nil {
				return bts, msgp.WrapError(err, "Header")
			}
		case seriesKeySegments:
			var blob []byte
			if blob, bts, err = msgp.ReadBytesZC(bts); err != nil {
				return bts, msgp.WrapError(err, "Segments")
			}
			if z.segs, _, err = ReadSegments(blob, 0); err != nil {
				return bts, msgp.WrapError(err, "Segments")
			}
		case seriesKeyLegacyPoints:
			return bts, ErrLegacySeries
		default:
			if bts, err = msgp.Skip(bts); err != nil {
				return bts, err
			}
		}
	}
	return bts, nil
}

// EncodeMsg implements msgp.Encodable.
func (z *Series) EncodeMsg(en *msgp.Writer) error {
	b, err := z.MarshalMsg(nil)
	if err != nil {
		return err
	}
	return en.Append(b...)
}

// DecodeMsg implements msgp.Decodable; the rows are copied from the reader.
func (z *Series) DecodeMsg(dc *msgp.Reader) error {
	var raw bytes.Buffer
	if _, err := dc.CopyNext(&raw); err != nil {
		return err
	}
	_, err := z.UnmarshalMsg(raw.Bytes())
	return err
}

// Msgsize returns an upper bound estimate of the number of bytes occupied by the serialized message.
func (z *Series) Msgsize() int {
	size := 1 + len(seriesKeyHeader) + 1 + z.Header.Msgsize() + len(seriesKeySegments) + 1 +
		bin32HeaderLen + binary.MaxVarintLen64 + int(z.segs.Size())
	for i := range z.segs {
		size += segmentOverhead + columnOverhead*len(z.segs[i].cols)
	}
	return size
}

// the most bytes the segment encoding adds beyond a Segment's memory: a Segment's counts and epochs'
// pad, and a column's kind, flags, pad and lengths
const (
	segmentOverhead = 2*binary.MaxVarintLen64 + wordSize
	columnOverhead  = 2 + wordSize + 2*binary.MaxVarintLen64
)
