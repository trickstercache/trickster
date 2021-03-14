/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package byterange

// Code generated by github.com/tinylib/msgp DO NOT EDIT.

import (
	"github.com/tinylib/msgp/msgp"
)

// DecodeMsg implements msgp.Decodable
func (z *Range) DecodeMsg(dc *msgp.Reader) (err error) {
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
		case "start":
			z.Start, err = dc.ReadInt64()
			if err != nil {
				err = msgp.WrapError(err, "Start")
				return
			}
		case "end":
			z.End, err = dc.ReadInt64()
			if err != nil {
				err = msgp.WrapError(err, "End")
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
func (z Range) EncodeMsg(en *msgp.Writer) (err error) {
	// map header, size 2
	// write "start"
	err = en.Append(0x82, 0xa5, 0x73, 0x74, 0x61, 0x72, 0x74)
	if err != nil {
		return
	}
	err = en.WriteInt64(z.Start)
	if err != nil {
		err = msgp.WrapError(err, "Start")
		return
	}
	// write "end"
	err = en.Append(0xa3, 0x65, 0x6e, 0x64)
	if err != nil {
		return
	}
	err = en.WriteInt64(z.End)
	if err != nil {
		err = msgp.WrapError(err, "End")
		return
	}
	return
}

// MarshalMsg implements msgp.Marshaler
func (z Range) MarshalMsg(b []byte) (o []byte, err error) {
	o = msgp.Require(b, z.Msgsize())
	// map header, size 2
	// string "start"
	o = append(o, 0x82, 0xa5, 0x73, 0x74, 0x61, 0x72, 0x74)
	o = msgp.AppendInt64(o, z.Start)
	// string "end"
	o = append(o, 0xa3, 0x65, 0x6e, 0x64)
	o = msgp.AppendInt64(o, z.End)
	return
}

// UnmarshalMsg implements msgp.Unmarshaler
func (z *Range) UnmarshalMsg(bts []byte) (o []byte, err error) {
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
		case "start":
			z.Start, bts, err = msgp.ReadInt64Bytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "Start")
				return
			}
		case "end":
			z.End, bts, err = msgp.ReadInt64Bytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "End")
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
func (z Range) Msgsize() (s int) {
	s = 1 + 6 + msgp.Int64Size + 4 + msgp.Int64Size
	return
}

// DecodeMsg implements msgp.Decodable
func (z *Ranges) DecodeMsg(dc *msgp.Reader) (err error) {
	var zb0002 uint32
	zb0002, err = dc.ReadArrayHeader()
	if err != nil {
		err = msgp.WrapError(err)
		return
	}
	if cap((*z)) >= int(zb0002) {
		(*z) = (*z)[:zb0002]
	} else {
		(*z) = make(Ranges, zb0002)
	}
	for zb0001 := range *z {
		var field []byte
		_ = field
		var zb0003 uint32
		zb0003, err = dc.ReadMapHeader()
		if err != nil {
			err = msgp.WrapError(err, zb0001)
			return
		}
		for zb0003 > 0 {
			zb0003--
			field, err = dc.ReadMapKeyPtr()
			if err != nil {
				err = msgp.WrapError(err, zb0001)
				return
			}
			switch msgp.UnsafeString(field) {
			case "start":
				(*z)[zb0001].Start, err = dc.ReadInt64()
				if err != nil {
					err = msgp.WrapError(err, zb0001, "Start")
					return
				}
			case "end":
				(*z)[zb0001].End, err = dc.ReadInt64()
				if err != nil {
					err = msgp.WrapError(err, zb0001, "End")
					return
				}
			default:
				err = dc.Skip()
				if err != nil {
					err = msgp.WrapError(err, zb0001)
					return
				}
			}
		}
	}
	return
}

// EncodeMsg implements msgp.Encodable
func (z Ranges) EncodeMsg(en *msgp.Writer) (err error) {
	err = en.WriteArrayHeader(uint32(len(z)))
	if err != nil {
		err = msgp.WrapError(err)
		return
	}
	for zb0004 := range z {
		// map header, size 2
		// write "start"
		err = en.Append(0x82, 0xa5, 0x73, 0x74, 0x61, 0x72, 0x74)
		if err != nil {
			return
		}
		err = en.WriteInt64(z[zb0004].Start)
		if err != nil {
			err = msgp.WrapError(err, zb0004, "Start")
			return
		}
		// write "end"
		err = en.Append(0xa3, 0x65, 0x6e, 0x64)
		if err != nil {
			return
		}
		err = en.WriteInt64(z[zb0004].End)
		if err != nil {
			err = msgp.WrapError(err, zb0004, "End")
			return
		}
	}
	return
}

// MarshalMsg implements msgp.Marshaler
func (z Ranges) MarshalMsg(b []byte) (o []byte, err error) {
	o = msgp.Require(b, z.Msgsize())
	o = msgp.AppendArrayHeader(o, uint32(len(z)))
	for zb0004 := range z {
		// map header, size 2
		// string "start"
		o = append(o, 0x82, 0xa5, 0x73, 0x74, 0x61, 0x72, 0x74)
		o = msgp.AppendInt64(o, z[zb0004].Start)
		// string "end"
		o = append(o, 0xa3, 0x65, 0x6e, 0x64)
		o = msgp.AppendInt64(o, z[zb0004].End)
	}
	return
}

// UnmarshalMsg implements msgp.Unmarshaler
func (z *Ranges) UnmarshalMsg(bts []byte) (o []byte, err error) {
	var zb0002 uint32
	zb0002, bts, err = msgp.ReadArrayHeaderBytes(bts)
	if err != nil {
		err = msgp.WrapError(err)
		return
	}
	if cap((*z)) >= int(zb0002) {
		(*z) = (*z)[:zb0002]
	} else {
		(*z) = make(Ranges, zb0002)
	}
	for zb0001 := range *z {
		var field []byte
		_ = field
		var zb0003 uint32
		zb0003, bts, err = msgp.ReadMapHeaderBytes(bts)
		if err != nil {
			err = msgp.WrapError(err, zb0001)
			return
		}
		for zb0003 > 0 {
			zb0003--
			field, bts, err = msgp.ReadMapKeyZC(bts)
			if err != nil {
				err = msgp.WrapError(err, zb0001)
				return
			}
			switch msgp.UnsafeString(field) {
			case "start":
				(*z)[zb0001].Start, bts, err = msgp.ReadInt64Bytes(bts)
				if err != nil {
					err = msgp.WrapError(err, zb0001, "Start")
					return
				}
			case "end":
				(*z)[zb0001].End, bts, err = msgp.ReadInt64Bytes(bts)
				if err != nil {
					err = msgp.WrapError(err, zb0001, "End")
					return
				}
			default:
				bts, err = msgp.Skip(bts)
				if err != nil {
					err = msgp.WrapError(err, zb0001)
					return
				}
			}
		}
	}
	o = bts
	return
}

// Msgsize returns an upper bound estimate of the number of bytes occupied by the serialized message
func (z Ranges) Msgsize() (s int) {
	s = msgp.ArrayHeaderSize + (len(z) * (11 + msgp.Int64Size + msgp.Int64Size))
	return
}
