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

// Package stream decodes upstream response bodies into Timeseries in a single
// pass, without first unmarshaling the whole body into an intermediate model.
package stream

import (
	"bytes"
	"errors"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Decoder consumes an upstream response body and builds a Timeseries from it.
// Feed it with Write calls and/or ReadFrom, then call Finish once.
type Decoder interface {
	io.Writer
	io.ReaderFrom
	// Finish completes the decode and returns the resulting Timeseries.
	Finish() (timeseries.Timeseries, error)
}

// NewDecoderFunc returns a Decoder for one response to the provided query.
type NewDecoderFunc func(*timeseries.TimeRangeQuery) (Decoder, error)

// FinishFunc completes a decode once all input has been consumed.
type FinishFunc func() (timeseries.Timeseries, error)

var (
	// ErrFinished indicates a Decoder was used after Finish.
	ErrFinished = errors.New("decoder already finished")
	// ErrInputConsumed indicates input was provided after a Decoder read its input to the end.
	ErrInputConsumed = errors.New("decoder input already consumed")
)

// ReaderUnmarshaler adapts newDecoder to a timeseries.UnmarshalerReaderFunc. It
// feeds the reader to the Decoder's ReadFrom, so the body is decoded as it is read.
func ReaderUnmarshaler(newDecoder NewDecoderFunc) timeseries.UnmarshalerReaderFunc {
	return func(r io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		if r == nil {
			return nil, timeseries.ErrInvalidBody
		}
		return decode(newDecoder, r, trq)
	}
}

// BytesUnmarshaler adapts newDecoder to a timeseries.UnmarshalerFunc.
func BytesUnmarshaler(newDecoder NewDecoderFunc) timeseries.UnmarshalerFunc {
	return func(b []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return decode(newDecoder, bytes.NewReader(b), trq)
	}
}

func decode(newDecoder NewDecoderFunc, r io.Reader,
	trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	dec, err := newDecoder(trq)
	if err != nil {
		return nil, err
	}
	if _, err = dec.ReadFrom(r); err != nil {
		return nil, err
	}
	ts, err := dec.Finish()
	if err != nil {
		return nil, err
	}
	if ts == nil {
		return nil, timeseries.ErrInvalidBody
	}
	return ts, nil
}
