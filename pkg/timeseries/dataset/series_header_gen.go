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

// Code generated by github.com/tinylib/msgp DO NOT EDIT.

import (
	"github.com/tinylib/msgp/msgp"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// DecodeMsg implements msgp.Decodable
func (z *SeriesHeader) DecodeMsg(dc *msgp.Reader) (err error) {
	var field []byte
	_ = field
	var zb0001 uint32
	zb0001, err = dc.ReadMapHeader()
	if err != nil {
		err = msgp.WrapError(err)
		return
	}
	for zb0001 > 0 {
		zb0001--
		field, err = dc.ReadMapKeyPtr()
		if err != nil {
			err = msgp.WrapError(err)
			return
		}
		switch msgp.UnsafeString(field) {
		case "name":
			z.Name, err = dc.ReadString()
			if err != nil {
				err = msgp.WrapError(err, "Name")
				return
			}
		case "tags":
			err = z.Tags.DecodeMsg(dc)
			if err != nil {
				err = msgp.WrapError(err, "Tags")
				return
			}
		case "fields":
			var zb0002 uint32
			zb0002, err = dc.ReadArrayHeader()
			if err != nil {
				err = msgp.WrapError(err, "FieldsList")
				return
			}
			if cap(z.FieldsList) >= int(zb0002) {
				z.FieldsList = (z.FieldsList)[:zb0002]
			} else {
				z.FieldsList = make([]timeseries.FieldDefinition, zb0002)
			}
			for za0001 := range z.FieldsList {
				err = z.FieldsList[za0001].DecodeMsg(dc)
				if err != nil {
					err = msgp.WrapError(err, "FieldsList", za0001)
					return
				}
			}
		case "ti":
			z.TimestampIndex, err = dc.ReadUint64()
			if err != nil {
				err = msgp.WrapError(err, "TimestampIndex")
				return
			}
		case "query":
			z.QueryStatement, err = dc.ReadString()
			if err != nil {
				err = msgp.WrapError(err, "QueryStatement")
				return
			}
		case "size":
			z.Size, err = dc.ReadInt()
			if err != nil {
				err = msgp.WrapError(err, "Size")
				return
			}
		default:
			err = dc.Skip()
			if err != nil {
				err = msgp.WrapError(err)
				return
			}
		}
	}
	return
}

// EncodeMsg implements msgp.Encodable
func (z *SeriesHeader) EncodeMsg(en *msgp.Writer) (err error) {
	// map header, size 6
	// write "name"
	err = en.Append(0x86, 0xa4, 0x6e, 0x61, 0x6d, 0x65)
	if err != nil {
		return
	}
	err = en.WriteString(z.Name)
	if err != nil {
		err = msgp.WrapError(err, "Name")
		return
	}
	// write "tags"
	err = en.Append(0xa4, 0x74, 0x61, 0x67, 0x73)
	if err != nil {
		return
	}
	err = z.Tags.EncodeMsg(en)
	if err != nil {
		err = msgp.WrapError(err, "Tags")
		return
	}
	// write "fields"
	err = en.Append(0xa6, 0x66, 0x69, 0x65, 0x6c, 0x64, 0x73)
	if err != nil {
		return
	}
	err = en.WriteArrayHeader(uint32(len(z.FieldsList)))
	if err != nil {
		err = msgp.WrapError(err, "FieldsList")
		return
	}
	for za0001 := range z.FieldsList {
		err = z.FieldsList[za0001].EncodeMsg(en)
		if err != nil {
			err = msgp.WrapError(err, "FieldsList", za0001)
			return
		}
	}
	// write "ti"
	err = en.Append(0xa2, 0x74, 0x69)
	if err != nil {
		return
	}
	err = en.WriteUint64(z.TimestampIndex)
	if err != nil {
		err = msgp.WrapError(err, "TimestampIndex")
		return
	}
	// write "query"
	err = en.Append(0xa5, 0x71, 0x75, 0x65, 0x72, 0x79)
	if err != nil {
		return
	}
	err = en.WriteString(z.QueryStatement)
	if err != nil {
		err = msgp.WrapError(err, "QueryStatement")
		return
	}
	// write "size"
	err = en.Append(0xa4, 0x73, 0x69, 0x7a, 0x65)
	if err != nil {
		return
	}
	err = en.WriteInt(z.Size)
	if err != nil {
		err = msgp.WrapError(err, "Size")
		return
	}
	return
}

// MarshalMsg implements msgp.Marshaler
func (z *SeriesHeader) MarshalMsg(b []byte) (o []byte, err error) {
	o = msgp.Require(b, z.Msgsize())
	// map header, size 6
	// string "name"
	o = append(o, 0x86, 0xa4, 0x6e, 0x61, 0x6d, 0x65)
	o = msgp.AppendString(o, z.Name)
	// string "tags"
	o = append(o, 0xa4, 0x74, 0x61, 0x67, 0x73)
	o, err = z.Tags.MarshalMsg(o)
	if err != nil {
		err = msgp.WrapError(err, "Tags")
		return
	}
	// string "fields"
	o = append(o, 0xa6, 0x66, 0x69, 0x65, 0x6c, 0x64, 0x73)
	o = msgp.AppendArrayHeader(o, uint32(len(z.FieldsList)))
	for za0001 := range z.FieldsList {
		o, err = z.FieldsList[za0001].MarshalMsg(o)
		if err != nil {
			err = msgp.WrapError(err, "FieldsList", za0001)
			return
		}
	}
	// string "ti"
	o = append(o, 0xa2, 0x74, 0x69)
	o = msgp.AppendUint64(o, z.TimestampIndex)
	// string "query"
	o = append(o, 0xa5, 0x71, 0x75, 0x65, 0x72, 0x79)
	o = msgp.AppendString(o, z.QueryStatement)
	// string "size"
	o = append(o, 0xa4, 0x73, 0x69, 0x7a, 0x65)
	o = msgp.AppendInt(o, z.Size)
	return
}

// UnmarshalMsg implements msgp.Unmarshaler
func (z *SeriesHeader) UnmarshalMsg(bts []byte) (o []byte, err error) {
	var field []byte
	_ = field
	var zb0001 uint32
	zb0001, bts, err = msgp.ReadMapHeaderBytes(bts)
	if err != nil {
		err = msgp.WrapError(err)
		return
	}
	for zb0001 > 0 {
		zb0001--
		field, bts, err = msgp.ReadMapKeyZC(bts)
		if err != nil {
			err = msgp.WrapError(err)
			return
		}
		switch msgp.UnsafeString(field) {
		case "name":
			z.Name, bts, err = msgp.ReadStringBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "Name")
				return
			}
		case "tags":
			bts, err = z.Tags.UnmarshalMsg(bts)
			if err != nil {
				err = msgp.WrapError(err, "Tags")
				return
			}
		case "fields":
			var zb0002 uint32
			zb0002, bts, err = msgp.ReadArrayHeaderBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "FieldsList")
				return
			}
			if cap(z.FieldsList) >= int(zb0002) {
				z.FieldsList = (z.FieldsList)[:zb0002]
			} else {
				z.FieldsList = make([]timeseries.FieldDefinition, zb0002)
			}
			for za0001 := range z.FieldsList {
				bts, err = z.FieldsList[za0001].UnmarshalMsg(bts)
				if err != nil {
					err = msgp.WrapError(err, "FieldsList", za0001)
					return
				}
			}
		case "ti":
			z.TimestampIndex, bts, err = msgp.ReadUint64Bytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "TimestampIndex")
				return
			}
		case "query":
			z.QueryStatement, bts, err = msgp.ReadStringBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "QueryStatement")
				return
			}
		case "size":
			z.Size, bts, err = msgp.ReadIntBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "Size")
				return
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
func (z *SeriesHeader) Msgsize() (s int) {
	s = 1 + 5 + msgp.StringPrefixSize + len(z.Name) + 5 + z.Tags.Msgsize() + 7 + msgp.ArrayHeaderSize
	for za0001 := range z.FieldsList {
		s += z.FieldsList[za0001].Msgsize()
	}
	s += 3 + msgp.Uint64Size + 6 + msgp.StringPrefixSize + len(z.QueryStatement) + 5 + msgp.IntSize
	return
}
