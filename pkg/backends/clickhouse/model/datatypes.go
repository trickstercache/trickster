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
