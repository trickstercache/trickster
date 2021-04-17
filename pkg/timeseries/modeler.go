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

//go:generate msgp

package timeseries

import "io"

// Modeler is a container object for Timeseries marshaling operations
type Modeler struct {
	WireUnmarshalerReader UnmarshalerReaderFunc `msg:"-"`
	WireUnmarshaler       UnmarshalerFunc       `msg:"-"`
	WireMarshaler         MarshalerFunc         `msg:"-"`
	WireMarshalWriter     MarshalWriterFunc     `msg:"-"`
	CacheUnmarshaler      UnmarshalerFunc       `msg:"-"`
	CacheMarshaler        MarshalerFunc         `msg:"-"`
}

// UnmarshalerFunc describes a function that unmarshals a Timeseries
type UnmarshalerFunc func([]byte, *TimeRangeQuery) (Timeseries, error)

// UnmarshalerReaderFunc describes a function that unmarshals a Timeseries from an io.Reader
type UnmarshalerReaderFunc func(io.Reader, *TimeRangeQuery) (Timeseries, error)

// MarshalerFunc describes a function that marshals a Timeseries
type MarshalerFunc func(Timeseries, *RequestOptions, int) ([]byte, error)

// MarshalWriterFunc describes a function that marshals a Timeseries to an io.Writer
type MarshalWriterFunc func(Timeseries, *RequestOptions, int, io.Writer) error

// NewModeler factories a modeler with the provided modeling functions
func NewModeler(
	wu UnmarshalerFunc, wur UnmarshalerReaderFunc,
	wm MarshalerFunc, wmw MarshalWriterFunc,
	cu UnmarshalerFunc, cm MarshalerFunc) *Modeler {
	return &Modeler{
		WireUnmarshaler:       wu,
		WireUnmarshalerReader: wur,
		WireMarshaler:         wm,
		WireMarshalWriter:     wmw,
		CacheUnmarshaler:      cu,
		CacheMarshaler:        cm,
	}
}
