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

package timeseries

// Code generated by github.com/tinylib/msgp DO NOT EDIT.

import (
	"github.com/tinylib/msgp/msgp"
)

// DecodeMsg implements msgp.Decodable
func (z *TimeRangeQuery) DecodeMsg(dc *msgp.Reader) (err error) {
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
		case "stmt":
			z.Statement, err = dc.ReadString()
			if err != nil {
				err = msgp.WrapError(err, "Statement")
				return
			}
		case "ex":
			err = z.Extent.DecodeMsg(dc)
			if err != nil {
				err = msgp.WrapError(err, "Extent")
				return
			}
		case "step":
			z.StepNS, err = dc.ReadInt64()
			if err != nil {
				err = msgp.WrapError(err, "StepNS")
				return
			}
		case "bft":
			z.BackfillToleranceNS, err = dc.ReadInt64()
			if err != nil {
				err = msgp.WrapError(err, "BackfillToleranceNS")
				return
			}
		case "rl":
			z.RecordLimit, err = dc.ReadInt()
			if err != nil {
				err = msgp.WrapError(err, "RecordLimit")
				return
			}
		case "tsdef":
			err = z.TimestampDefinition.DecodeMsg(dc)
			if err != nil {
				err = msgp.WrapError(err, "TimestampDefinition")
				return
			}
		case "tfdefs":
			var zb0002 uint32
			zb0002, err = dc.ReadArrayHeader()
			if err != nil {
				err = msgp.WrapError(err, "TagFieldDefintions")
				return
			}
			if cap(z.TagFieldDefintions) >= int(zb0002) {
				z.TagFieldDefintions = (z.TagFieldDefintions)[:zb0002]
			} else {
				z.TagFieldDefintions = make([]FieldDefinition, zb0002)
			}
			for za0001 := range z.TagFieldDefintions {
				err = z.TagFieldDefintions[za0001].DecodeMsg(dc)
				if err != nil {
					err = msgp.WrapError(err, "TagFieldDefintions", za0001)
					return
				}
			}
		case "vfdefs":
			var zb0003 uint32
			zb0003, err = dc.ReadArrayHeader()
			if err != nil {
				err = msgp.WrapError(err, "ValueFieldDefinitions")
				return
			}
			if cap(z.ValueFieldDefinitions) >= int(zb0003) {
				z.ValueFieldDefinitions = (z.ValueFieldDefinitions)[:zb0003]
			} else {
				z.ValueFieldDefinitions = make([]FieldDefinition, zb0003)
			}
			for za0002 := range z.ValueFieldDefinitions {
				err = z.ValueFieldDefinitions[za0002].DecodeMsg(dc)
				if err != nil {
					err = msgp.WrapError(err, "ValueFieldDefinitions", za0002)
					return
				}
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
func (z *TimeRangeQuery) EncodeMsg(en *msgp.Writer) (err error) {
	// map header, size 8
	// write "stmt"
	err = en.Append(0x88, 0xa4, 0x73, 0x74, 0x6d, 0x74)
	if err != nil {
		return
	}
	err = en.WriteString(z.Statement)
	if err != nil {
		err = msgp.WrapError(err, "Statement")
		return
	}
	// write "ex"
	err = en.Append(0xa2, 0x65, 0x78)
	if err != nil {
		return
	}
	err = z.Extent.EncodeMsg(en)
	if err != nil {
		err = msgp.WrapError(err, "Extent")
		return
	}
	// write "step"
	err = en.Append(0xa4, 0x73, 0x74, 0x65, 0x70)
	if err != nil {
		return
	}
	err = en.WriteInt64(z.StepNS)
	if err != nil {
		err = msgp.WrapError(err, "StepNS")
		return
	}
	// write "bft"
	err = en.Append(0xa3, 0x62, 0x66, 0x74)
	if err != nil {
		return
	}
	err = en.WriteInt64(z.BackfillToleranceNS)
	if err != nil {
		err = msgp.WrapError(err, "BackfillToleranceNS")
		return
	}
	// write "rl"
	err = en.Append(0xa2, 0x72, 0x6c)
	if err != nil {
		return
	}
	err = en.WriteInt(z.RecordLimit)
	if err != nil {
		err = msgp.WrapError(err, "RecordLimit")
		return
	}
	// write "tsdef"
	err = en.Append(0xa5, 0x74, 0x73, 0x64, 0x65, 0x66)
	if err != nil {
		return
	}
	err = z.TimestampDefinition.EncodeMsg(en)
	if err != nil {
		err = msgp.WrapError(err, "TimestampDefinition")
		return
	}
	// write "tfdefs"
	err = en.Append(0xa6, 0x74, 0x66, 0x64, 0x65, 0x66, 0x73)
	if err != nil {
		return
	}
	err = en.WriteArrayHeader(uint32(len(z.TagFieldDefintions)))
	if err != nil {
		err = msgp.WrapError(err, "TagFieldDefintions")
		return
	}
	for za0001 := range z.TagFieldDefintions {
		err = z.TagFieldDefintions[za0001].EncodeMsg(en)
		if err != nil {
			err = msgp.WrapError(err, "TagFieldDefintions", za0001)
			return
		}
	}
	// write "vfdefs"
	err = en.Append(0xa6, 0x76, 0x66, 0x64, 0x65, 0x66, 0x73)
	if err != nil {
		return
	}
	err = en.WriteArrayHeader(uint32(len(z.ValueFieldDefinitions)))
	if err != nil {
		err = msgp.WrapError(err, "ValueFieldDefinitions")
		return
	}
	for za0002 := range z.ValueFieldDefinitions {
		err = z.ValueFieldDefinitions[za0002].EncodeMsg(en)
		if err != nil {
			err = msgp.WrapError(err, "ValueFieldDefinitions", za0002)
			return
		}
	}
	return
}

// MarshalMsg implements msgp.Marshaler
func (z *TimeRangeQuery) MarshalMsg(b []byte) (o []byte, err error) {
	o = msgp.Require(b, z.Msgsize())
	// map header, size 8
	// string "stmt"
	o = append(o, 0x88, 0xa4, 0x73, 0x74, 0x6d, 0x74)
	o = msgp.AppendString(o, z.Statement)
	// string "ex"
	o = append(o, 0xa2, 0x65, 0x78)
	o, err = z.Extent.MarshalMsg(o)
	if err != nil {
		err = msgp.WrapError(err, "Extent")
		return
	}
	// string "step"
	o = append(o, 0xa4, 0x73, 0x74, 0x65, 0x70)
	o = msgp.AppendInt64(o, z.StepNS)
	// string "bft"
	o = append(o, 0xa3, 0x62, 0x66, 0x74)
	o = msgp.AppendInt64(o, z.BackfillToleranceNS)
	// string "rl"
	o = append(o, 0xa2, 0x72, 0x6c)
	o = msgp.AppendInt(o, z.RecordLimit)
	// string "tsdef"
	o = append(o, 0xa5, 0x74, 0x73, 0x64, 0x65, 0x66)
	o, err = z.TimestampDefinition.MarshalMsg(o)
	if err != nil {
		err = msgp.WrapError(err, "TimestampDefinition")
		return
	}
	// string "tfdefs"
	o = append(o, 0xa6, 0x74, 0x66, 0x64, 0x65, 0x66, 0x73)
	o = msgp.AppendArrayHeader(o, uint32(len(z.TagFieldDefintions)))
	for za0001 := range z.TagFieldDefintions {
		o, err = z.TagFieldDefintions[za0001].MarshalMsg(o)
		if err != nil {
			err = msgp.WrapError(err, "TagFieldDefintions", za0001)
			return
		}
	}
	// string "vfdefs"
	o = append(o, 0xa6, 0x76, 0x66, 0x64, 0x65, 0x66, 0x73)
	o = msgp.AppendArrayHeader(o, uint32(len(z.ValueFieldDefinitions)))
	for za0002 := range z.ValueFieldDefinitions {
		o, err = z.ValueFieldDefinitions[za0002].MarshalMsg(o)
		if err != nil {
			err = msgp.WrapError(err, "ValueFieldDefinitions", za0002)
			return
		}
	}
	return
}

// UnmarshalMsg implements msgp.Unmarshaler
func (z *TimeRangeQuery) UnmarshalMsg(bts []byte) (o []byte, err error) {
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
		case "stmt":
			z.Statement, bts, err = msgp.ReadStringBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "Statement")
				return
			}
		case "ex":
			bts, err = z.Extent.UnmarshalMsg(bts)
			if err != nil {
				err = msgp.WrapError(err, "Extent")
				return
			}
		case "step":
			z.StepNS, bts, err = msgp.ReadInt64Bytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "StepNS")
				return
			}
		case "bft":
			z.BackfillToleranceNS, bts, err = msgp.ReadInt64Bytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "BackfillToleranceNS")
				return
			}
		case "rl":
			z.RecordLimit, bts, err = msgp.ReadIntBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "RecordLimit")
				return
			}
		case "tsdef":
			bts, err = z.TimestampDefinition.UnmarshalMsg(bts)
			if err != nil {
				err = msgp.WrapError(err, "TimestampDefinition")
				return
			}
		case "tfdefs":
			var zb0002 uint32
			zb0002, bts, err = msgp.ReadArrayHeaderBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "TagFieldDefintions")
				return
			}
			if cap(z.TagFieldDefintions) >= int(zb0002) {
				z.TagFieldDefintions = (z.TagFieldDefintions)[:zb0002]
			} else {
				z.TagFieldDefintions = make([]FieldDefinition, zb0002)
			}
			for za0001 := range z.TagFieldDefintions {
				bts, err = z.TagFieldDefintions[za0001].UnmarshalMsg(bts)
				if err != nil {
					err = msgp.WrapError(err, "TagFieldDefintions", za0001)
					return
				}
			}
		case "vfdefs":
			var zb0003 uint32
			zb0003, bts, err = msgp.ReadArrayHeaderBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "ValueFieldDefinitions")
				return
			}
			if cap(z.ValueFieldDefinitions) >= int(zb0003) {
				z.ValueFieldDefinitions = (z.ValueFieldDefinitions)[:zb0003]
			} else {
				z.ValueFieldDefinitions = make([]FieldDefinition, zb0003)
			}
			for za0002 := range z.ValueFieldDefinitions {
				bts, err = z.ValueFieldDefinitions[za0002].UnmarshalMsg(bts)
				if err != nil {
					err = msgp.WrapError(err, "ValueFieldDefinitions", za0002)
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
func (z *TimeRangeQuery) Msgsize() (s int) {
	s = 1 + 5 + msgp.StringPrefixSize + len(z.Statement) + 3 + z.Extent.Msgsize() + 5 + msgp.Int64Size + 4 + msgp.Int64Size + 3 + msgp.IntSize + 6 + z.TimestampDefinition.Msgsize() + 7 + msgp.ArrayHeaderSize
	for za0001 := range z.TagFieldDefintions {
		s += z.TagFieldDefintions[za0001].Msgsize()
	}
	s += 7 + msgp.ArrayHeaderSize
	for za0002 := range z.ValueFieldDefinitions {
		s += z.ValueFieldDefinitions[za0002].Msgsize()
	}
	return
}
