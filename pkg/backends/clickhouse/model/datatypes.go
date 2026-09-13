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

package model

type DataType int

// ClickHouse type name strings used across marshal/unmarshal.
const (
	TypeUInt8    = "UInt8"
	TypeUInt16   = "UInt16"
	TypeUInt32   = "UInt32"
	TypeUInt64   = "UInt64"
	TypeInt8     = "Int8"
	TypeInt16    = "Int16"
	TypeInt32    = "Int32"
	TypeInt64    = "Int64"
	TypeFloat32  = "Float32"
	TypeFloat64  = "Float64"
	TypeDateTime = "DateTime"
	TypeDate     = "Date"
	TypeString   = "String"
	TypeBool     = "Bool"
)

const (
	UInt8 = DataType(iota)
	UInt16
	UInt32
	UInt64
	UInt256
	Int8
	Int16
	Int32
	Int64
	Int128
	Int256

	Float32
	Float64

	Decimal

	Boolean

	String
	FixedString

	UUID

	Date
	DateTime
	DateTime64

	Enum
	LowCardinality
	Array
	AggregateFunction
	Tuple
	Nullable
)
