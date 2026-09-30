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
	"github.com/tinylib/msgp/msgp"
)

// Points' msgpack methods are written here rather than generated, so a decode can cut its points'
// values from shared chunks instead of allocating a slice per point

// the most values one decode chunk holds, so a cropped view keeps little else alive
const maxDecodeChunk = 4096

// DecodeMsg implements msgp.Decodable
func (z *Points) DecodeMsg(dc *msgp.Reader) (err error) {
	var n uint32
	n, err = dc.ReadArrayHeader()
	if err != nil {
		err = msgp.WrapError(err)
		return
	}
	if cap(*z) >= int(n) {
		*z = (*z)[:n]
	} else {
		*z = make(Points, n)
	}
	for i := range *z {
		err = (*z)[i].DecodeMsg(dc)
		if err != nil {
			err = msgp.WrapError(err, i)
			return
		}
	}
	return
}

// EncodeMsg implements msgp.Encodable
func (z Points) EncodeMsg(en *msgp.Writer) (err error) {
	err = en.WriteArrayHeader(uint32(len(z))) // #nosec G115 -- a slice's length fits
	if err != nil {
		err = msgp.WrapError(err)
		return
	}
	for i := range z {
		err = z[i].EncodeMsg(en)
		if err != nil {
			err = msgp.WrapError(err, i)
			return
		}
	}
	return
}

// MarshalMsg implements msgp.Marshaler
func (z Points) MarshalMsg(b []byte) (o []byte, err error) {
	o = msgp.Require(b, z.Msgsize())
	o = msgp.AppendArrayHeader(o, uint32(len(z))) // #nosec G115 -- a slice's length fits
	for i := range z {
		o, err = z[i].MarshalMsg(o)
		if err != nil {
			err = msgp.WrapError(err, i)
			return
		}
	}
	return
}

// UnmarshalMsg implements msgp.Unmarshaler. Each point's values are a capped sub-slice of a chunk
// shared with its neighbors, so an append to one never overwrites the next
func (z *Points) UnmarshalMsg(bts []byte) (o []byte, err error) {
	var n uint32
	n, bts, err = msgp.ReadArrayHeaderBytes(bts)
	if err != nil {
		err = msgp.WrapError(err)
		return
	}
	if cap(*z) >= int(n) {
		*z = (*z)[:n]
	} else {
		*z = make(Points, n)
	}
	var chunk []any
	for i := range *z {
		bts, err = (*z)[i].unmarshalMsgFrom(bts, &chunk, len(*z)-i)
		if err != nil {
			err = msgp.WrapError(err, i)
			return
		}
	}
	o = bts
	return
}

// decodes as the generated UnmarshalMsg does, taking values from chunk, which it refills with room
// for as many more points of this width, up to maxDecodeChunk, when short
func (z *Point) unmarshalMsgFrom(bts []byte, chunk *[]any, remaining int) (o []byte, err error) {
	var fields uint32
	fields, bts, err = msgp.ReadMapHeaderBytes(bts)
	if err != nil {
		err = msgp.WrapError(err)
		return
	}
	var field []byte
	for ; fields > 0; fields-- {
		field, bts, err = msgp.ReadMapKeyZC(bts)
		if err != nil {
			err = msgp.WrapError(err)
			return
		}
		switch msgp.UnsafeString(field) {
		case "epoch":
			bts, err = z.Epoch.UnmarshalMsg(bts)
			if err != nil {
				err = msgp.WrapError(err, "Epoch")
				return
			}
		case "size":
			z.Size, bts, err = msgp.ReadIntBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "Size")
				return
			}
		case "values":
			var w uint32
			w, bts, err = msgp.ReadArrayHeaderBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "Values")
				return
			}
			width := int(w)
			if width > len(bts) {
				// every value takes at least a byte, so a corrupt header can't make it allocate
				err = msgp.WrapError(msgp.ErrShortBytes, "Values")
				return
			}
			if width == 0 {
				// as the generated decode does, which leaves a new point's values nil
				z.Values = z.Values[:0]
				continue
			}
			if len(*chunk) < width {
				*chunk = make([]any, max(width, min(width*remaining, maxDecodeChunk)))
			}
			z.Values = (*chunk)[:width:width]
			*chunk = (*chunk)[width:]
			for j := range z.Values {
				z.Values[j], bts, err = msgp.ReadIntfBytes(bts)
				if err != nil {
					err = msgp.WrapError(err, "Values", j)
					return
				}
			}
		default:
			bts, err = msgp.Skip(bts)
			if err != nil {
				err = msgp.WrapError(err)
				return
			}
		}
	}
	o = bts
	return
}

// Msgsize returns an upper bound estimate of the number of bytes occupied by the serialized message
func (z Points) Msgsize() (s int) {
	s = msgp.ArrayHeaderSize
	for i := range z {
		s += z[i].Msgsize()
	}
	return
}
