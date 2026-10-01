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

package stream

import (
	"errors"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Sniff returns a Decoder for a body whose format its first byte tells: pick returns the Decoder
// that is given all of the body. Finish fails with timeseries.ErrInvalidBody for an empty body.
func Sniff(pick func(first byte) Decoder) Decoder {
	return &sniffer{pick: pick}
}

type sniffer struct {
	pick func(first byte) Decoder
	dec  Decoder
	err  error
	done bool
}

func (s *sniffer) Write(p []byte) (int, error) {
	if err := s.check(); err != nil {
		return 0, err
	}
	if s.dec == nil {
		if len(p) == 0 {
			return 0, nil
		}
		s.dec = s.pick(p[0])
	}
	return s.dec.Write(p)
}

func (s *sniffer) ReadFrom(r io.Reader) (int64, error) {
	if err := s.check(); err != nil {
		return 0, err
	}
	if s.dec != nil {
		return s.dec.ReadFrom(r)
	}
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, nil
		}
		s.err = err
		return 0, err
	}
	s.dec = s.pick(first[0])
	if _, err := s.dec.Write(first[:]); err != nil {
		return 1, err
	}
	n, err := s.dec.ReadFrom(r)
	return n + 1, err
}

func (s *sniffer) Finish() (timeseries.Timeseries, error) {
	if s.done {
		return nil, ErrFinished
	}
	s.done = true
	switch {
	case s.err != nil:
		return nil, s.err
	case s.dec == nil:
		return nil, timeseries.ErrInvalidBody
	}
	return s.dec.Finish()
}

func (s *sniffer) check() error {
	if s.done {
		return ErrFinished
	}
	return s.err
}
