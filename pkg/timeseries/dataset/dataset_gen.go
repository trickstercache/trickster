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
func (z *DataSet) DecodeMsg(dc *msgp.Reader) (err error) {
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
		case "status":
			z.Status, err = dc.ReadString()
			if err != nil {
				err = msgp.WrapError(err, "Status")
				return
			}
		case "extent_list":
			err = z.ExtentList.DecodeMsg(dc)
			if err != nil {
				err = msgp.WrapError(err, "ExtentList")
				return
			}
		case "results":
			var zb0002 uint32
			zb0002, err = dc.ReadArrayHeader()
			if err != nil {
				err = msgp.WrapError(err, "Results")
				return
			}
			if cap(z.Results) >= int(zb0002) {
				z.Results = (z.Results)[:zb0002]
			} else {
				z.Results = make([]*Result, zb0002)
			}
			for za0001 := range z.Results {
				if dc.IsNil() {
					err = dc.ReadNil()
					if err != nil {
						err = msgp.WrapError(err, "Results", za0001)
						return
					}
					z.Results[za0001] = nil
				} else {
					if z.Results[za0001] == nil {
						z.Results[za0001] = new(Result)
					}
					err = z.Results[za0001].DecodeMsg(dc)
					if err != nil {
						err = msgp.WrapError(err, "Results", za0001)
						return
					}
				}
			}
		case "error":
			z.Error, err = dc.ReadString()
			if err != nil {
				err = msgp.WrapError(err, "Error")
				return
			}
		case "errorType":
			z.ErrorType, err = dc.ReadString()
			if err != nil {
				err = msgp.WrapError(err, "ErrorType")
				return
			}
		case "warnings":
			var zb0003 uint32
			zb0003, err = dc.ReadArrayHeader()
			if err != nil {
				err = msgp.WrapError(err, "Warnings")
				return
			}
			if cap(z.Warnings) >= int(zb0003) {
				z.Warnings = (z.Warnings)[:zb0003]
			} else {
				z.Warnings = make([]string, zb0003)
			}
			for za0002 := range z.Warnings {
				z.Warnings[za0002], err = dc.ReadString()
				if err != nil {
					err = msgp.WrapError(err, "Warnings", za0002)
					return
				}
			}
		case "trq":
			if dc.IsNil() {
				err = dc.ReadNil()
				if err != nil {
					err = msgp.WrapError(err, "TimeRangeQuery")
					return
				}
				z.TimeRangeQuery = nil
			} else {
				if z.TimeRangeQuery == nil {
					z.TimeRangeQuery = new(timeseries.TimeRangeQuery)
				}
				err = z.TimeRangeQuery.DecodeMsg(dc)
				if err != nil {
					err = msgp.WrapError(err, "TimeRangeQuery")
					return
				}
			}
		case "volatile_extents":
			err = z.VolatileExtentList.DecodeMsg(dc)
			if err != nil {
				err = msgp.WrapError(err, "VolatileExtentList")
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
func (z *DataSet) EncodeMsg(en *msgp.Writer) (err error) {
	// map header, size 8
	// write "status"
	err = en.Append(0x88, 0xa6, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73)
	if err != nil {
		return
	}
	err = en.WriteString(z.Status)
	if err != nil {
		err = msgp.WrapError(err, "Status")
		return
	}
	// write "extent_list"
	err = en.Append(0xab, 0x65, 0x78, 0x74, 0x65, 0x6e, 0x74, 0x5f, 0x6c, 0x69, 0x73, 0x74)
	if err != nil {
		return
	}
	err = z.ExtentList.EncodeMsg(en)
	if err != nil {
		err = msgp.WrapError(err, "ExtentList")
		return
	}
	// write "results"
	err = en.Append(0xa7, 0x72, 0x65, 0x73, 0x75, 0x6c, 0x74, 0x73)
	if err != nil {
		return
	}
	err = en.WriteArrayHeader(uint32(len(z.Results)))
	if err != nil {
		err = msgp.WrapError(err, "Results")
		return
	}
	for za0001 := range z.Results {
		if z.Results[za0001] == nil {
			err = en.WriteNil()
			if err != nil {
				return
			}
		} else {
			err = z.Results[za0001].EncodeMsg(en)
			if err != nil {
				err = msgp.WrapError(err, "Results", za0001)
				return
			}
		}
	}
	// write "error"
	err = en.Append(0xa5, 0x65, 0x72, 0x72, 0x6f, 0x72)
	if err != nil {
		return
	}
	err = en.WriteString(z.Error)
	if err != nil {
		err = msgp.WrapError(err, "Error")
		return
	}
	// write "errorType"
	err = en.Append(0xa9, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x54, 0x79, 0x70, 0x65)
	if err != nil {
		return
	}
	err = en.WriteString(z.ErrorType)
	if err != nil {
		err = msgp.WrapError(err, "ErrorType")
		return
	}
	// write "warnings"
	err = en.Append(0xa8, 0x77, 0x61, 0x72, 0x6e, 0x69, 0x6e, 0x67, 0x73)
	if err != nil {
		return
	}
	err = en.WriteArrayHeader(uint32(len(z.Warnings)))
	if err != nil {
		err = msgp.WrapError(err, "Warnings")
		return
	}
	for za0002 := range z.Warnings {
		err = en.WriteString(z.Warnings[za0002])
		if err != nil {
			err = msgp.WrapError(err, "Warnings", za0002)
			return
		}
	}
	// write "trq"
	err = en.Append(0xa3, 0x74, 0x72, 0x71)
	if err != nil {
		return
	}
	if z.TimeRangeQuery == nil {
		err = en.WriteNil()
		if err != nil {
			return
		}
	} else {
		err = z.TimeRangeQuery.EncodeMsg(en)
		if err != nil {
			err = msgp.WrapError(err, "TimeRangeQuery")
			return
		}
	}
	// write "volatile_extents"
	err = en.Append(0xb0, 0x76, 0x6f, 0x6c, 0x61, 0x74, 0x69, 0x6c, 0x65, 0x5f, 0x65, 0x78, 0x74, 0x65, 0x6e, 0x74, 0x73)
	if err != nil {
		return
	}
	err = z.VolatileExtentList.EncodeMsg(en)
	if err != nil {
		err = msgp.WrapError(err, "VolatileExtentList")
		return
	}
	return
}

// MarshalMsg implements msgp.Marshaler
func (z *DataSet) MarshalMsg(b []byte) (o []byte, err error) {
	o = msgp.Require(b, z.Msgsize())
	// map header, size 8
	// string "status"
	o = append(o, 0x88, 0xa6, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73)
	o = msgp.AppendString(o, z.Status)
	// string "extent_list"
	o = append(o, 0xab, 0x65, 0x78, 0x74, 0x65, 0x6e, 0x74, 0x5f, 0x6c, 0x69, 0x73, 0x74)
	o, err = z.ExtentList.MarshalMsg(o)
	if err != nil {
		err = msgp.WrapError(err, "ExtentList")
		return
	}
	// string "results"
	o = append(o, 0xa7, 0x72, 0x65, 0x73, 0x75, 0x6c, 0x74, 0x73)
	o = msgp.AppendArrayHeader(o, uint32(len(z.Results)))
	for za0001 := range z.Results {
		if z.Results[za0001] == nil {
			o = msgp.AppendNil(o)
		} else {
			o, err = z.Results[za0001].MarshalMsg(o)
			if err != nil {
				err = msgp.WrapError(err, "Results", za0001)
				return
			}
		}
	}
	// string "error"
	o = append(o, 0xa5, 0x65, 0x72, 0x72, 0x6f, 0x72)
	o = msgp.AppendString(o, z.Error)
	// string "errorType"
	o = append(o, 0xa9, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x54, 0x79, 0x70, 0x65)
	o = msgp.AppendString(o, z.ErrorType)
	// string "warnings"
	o = append(o, 0xa8, 0x77, 0x61, 0x72, 0x6e, 0x69, 0x6e, 0x67, 0x73)
	o = msgp.AppendArrayHeader(o, uint32(len(z.Warnings)))
	for za0002 := range z.Warnings {
		o = msgp.AppendString(o, z.Warnings[za0002])
	}
	// string "trq"
	o = append(o, 0xa3, 0x74, 0x72, 0x71)
	if z.TimeRangeQuery == nil {
		o = msgp.AppendNil(o)
	} else {
		o, err = z.TimeRangeQuery.MarshalMsg(o)
		if err != nil {
			err = msgp.WrapError(err, "TimeRangeQuery")
			return
		}
	}
	// string "volatile_extents"
	o = append(o, 0xb0, 0x76, 0x6f, 0x6c, 0x61, 0x74, 0x69, 0x6c, 0x65, 0x5f, 0x65, 0x78, 0x74, 0x65, 0x6e, 0x74, 0x73)
	o, err = z.VolatileExtentList.MarshalMsg(o)
	if err != nil {
		err = msgp.WrapError(err, "VolatileExtentList")
		return
	}
	return
}

// UnmarshalMsg implements msgp.Unmarshaler
func (z *DataSet) UnmarshalMsg(bts []byte) (o []byte, err error) {
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
		case "status":
			z.Status, bts, err = msgp.ReadStringBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "Status")
				return
			}
		case "extent_list":
			bts, err = z.ExtentList.UnmarshalMsg(bts)
			if err != nil {
				err = msgp.WrapError(err, "ExtentList")
				return
			}
		case "results":
			var zb0002 uint32
			zb0002, bts, err = msgp.ReadArrayHeaderBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "Results")
				return
			}
			if cap(z.Results) >= int(zb0002) {
				z.Results = (z.Results)[:zb0002]
			} else {
				z.Results = make([]*Result, zb0002)
			}
			for za0001 := range z.Results {
				if msgp.IsNil(bts) {
					bts, err = msgp.ReadNilBytes(bts)
					if err != nil {
						return
					}
					z.Results[za0001] = nil
				} else {
					if z.Results[za0001] == nil {
						z.Results[za0001] = new(Result)
					}
					bts, err = z.Results[za0001].UnmarshalMsg(bts)
					if err != nil {
						err = msgp.WrapError(err, "Results", za0001)
						return
					}
				}
			}
		case "error":
			z.Error, bts, err = msgp.ReadStringBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "Error")
				return
			}
		case "errorType":
			z.ErrorType, bts, err = msgp.ReadStringBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "ErrorType")
				return
			}
		case "warnings":
			var zb0003 uint32
			zb0003, bts, err = msgp.ReadArrayHeaderBytes(bts)
			if err != nil {
				err = msgp.WrapError(err, "Warnings")
				return
			}
			if cap(z.Warnings) >= int(zb0003) {
				z.Warnings = (z.Warnings)[:zb0003]
			} else {
				z.Warnings = make([]string, zb0003)
			}
			for za0002 := range z.Warnings {
				z.Warnings[za0002], bts, err = msgp.ReadStringBytes(bts)
				if err != nil {
					err = msgp.WrapError(err, "Warnings", za0002)
					return
				}
			}
		case "trq":
			if msgp.IsNil(bts) {
				bts, err = msgp.ReadNilBytes(bts)
				if err != nil {
					return
				}
				z.TimeRangeQuery = nil
			} else {
				if z.TimeRangeQuery == nil {
					z.TimeRangeQuery = new(timeseries.TimeRangeQuery)
				}
				bts, err = z.TimeRangeQuery.UnmarshalMsg(bts)
				if err != nil {
					err = msgp.WrapError(err, "TimeRangeQuery")
					return
				}
			}
		case "volatile_extents":
			bts, err = z.VolatileExtentList.UnmarshalMsg(bts)
			if err != nil {
				err = msgp.WrapError(err, "VolatileExtentList")
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
func (z *DataSet) Msgsize() (s int) {
	s = 1 + 7 + msgp.StringPrefixSize + len(z.Status) + 12 + z.ExtentList.Msgsize() + 8 + msgp.ArrayHeaderSize
	for za0001 := range z.Results {
		if z.Results[za0001] == nil {
			s += msgp.NilSize
		} else {
			s += z.Results[za0001].Msgsize()
		}
	}
	s += 6 + msgp.StringPrefixSize + len(z.Error) + 10 + msgp.StringPrefixSize + len(z.ErrorType) + 9 + msgp.ArrayHeaderSize
	for za0002 := range z.Warnings {
		s += msgp.StringPrefixSize + len(z.Warnings[za0002])
	}
	s += 4
	if z.TimeRangeQuery == nil {
		s += msgp.NilSize
	} else {
		s += z.TimeRangeQuery.Msgsize()
	}
	s += 17 + z.VolatileExtentList.Msgsize()
	return
}
