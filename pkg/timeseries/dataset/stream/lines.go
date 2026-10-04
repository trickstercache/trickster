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
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// DefaultMaxLineBytes is the longest line, excluding its terminator, that a
// Lines decoder accepts unless SetMaxLineBytes is used.
const DefaultMaxLineBytes = 16 << 20

const readBufferSize = 32 << 10

// ErrLineTooLong indicates a line exceeded the decoder's maximum line length.
var ErrLineTooLong = fmt.Errorf("%w: line exceeds maximum length", timeseries.ErrInvalidBody)

var readBuffers = sync.Pool{New: func() any {
	b := make([]byte, readBufferSize)
	return &b
}}

// Lines is a Decoder for newline-delimited formats such as TSV and JSON Lines.
// Each line is passed to its callback without the "\n" or "\r\n" terminator.
type Lines struct {
	onLine  func(line []byte) error
	finish  FinishFunc
	partial []byte
	max     int
	err     error
	done    bool
}

var _ Decoder = (*Lines)(nil)

// NewLines returns a Lines decoder. onLine must not retain line after returning,
// and finish is called by Finish after the last line.
func NewLines(onLine func(line []byte) error, finish FinishFunc) *Lines {
	return &Lines{onLine: onLine, finish: finish, max: DefaultMaxLineBytes}
}

// SetMaxLineBytes sets the longest line, excluding its terminator, that the
// decoder accepts. Values below 1 restore DefaultMaxLineBytes.
func (l *Lines) SetMaxLineBytes(n int) *Lines {
	l.max = n
	if n < 1 {
		l.max = DefaultMaxLineBytes
	}
	return l
}

// Write passes each complete line in p to the callback and holds any trailing
// partial line until more input or Finish arrives.
func (l *Lines) Write(p []byte) (int, error) {
	if err := l.check(); err != nil {
		return 0, err
	}
	rest := p
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			if !l.fits(rest) {
				return len(p) - len(rest), l.fail(ErrLineTooLong)
			}
			l.partial = append(l.partial, rest...)
			break
		}
		line := rest[:i]
		if len(l.partial) > 0 {
			// check before joining, so an overlong line never grows the partial buffer
			if !l.fits(line) {
				return len(p) - len(rest), l.fail(ErrLineTooLong)
			}
			l.partial = append(l.partial, line...)
			line = l.partial
		}
		if err := l.emit(line); err != nil {
			return len(p) - len(rest), err
		}
		l.partial = l.partial[:0]
		rest = rest[i+1:]
	}
	return len(p), nil
}

// ReadFrom reads r to EOF, passing each line to the callback. A reader that
// implements io.WriterTo writes into the decoder directly, avoiding a copy.
func (l *Lines) ReadFrom(r io.Reader) (int64, error) {
	if err := l.check(); err != nil {
		return 0, err
	}
	if wt, ok := r.(io.WriterTo); ok {
		n, err := wt.WriteTo(l)
		if err != nil {
			return n, l.fail(err)
		}
		return n, nil
	}
	bp := readBuffers.Get().(*[]byte)
	defer readBuffers.Put(bp)
	buf := *bp
	var total int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			total += int64(n)
			if _, werr := l.Write(buf[:n]); werr != nil {
				return total, werr
			}
		}
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			return total, l.fail(err)
		}
	}
}

// Finish passes any final unterminated line to the callback, then calls the
// finish function.
func (l *Lines) Finish() (timeseries.Timeseries, error) {
	if l.done {
		return nil, ErrFinished
	}
	l.done = true
	if l.err != nil {
		return nil, l.err
	}
	if len(l.partial) > 0 {
		if err := l.emit(l.partial); err != nil {
			return nil, err
		}
	}
	l.partial = nil
	return l.finish()
}

func (l *Lines) check() error {
	if l.done {
		return ErrFinished
	}
	return l.err
}

func (l *Lines) fail(err error) error {
	if l.err == nil {
		l.err = err
	}
	return l.err
}

func (l *Lines) fits(next []byte) bool {
	// the partial line plus next, less a trailing "\r" whose "\n" may follow
	n := len(l.partial) + len(next)
	var last byte
	switch {
	case len(next) > 0:
		last = next[len(next)-1]
	case len(l.partial) > 0:
		last = l.partial[len(l.partial)-1]
	}
	if last == '\r' {
		n--
	}
	return n <= l.max
}

func (l *Lines) emit(raw []byte) error {
	line := raw
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	if len(line) > l.max {
		return l.fail(ErrLineTooLong)
	}
	if err := l.onLine(line); err != nil {
		return l.fail(err)
	}
	return nil
}

// SplitFields splits line at each sep into dst, reusing its capacity. Quotes
// and escapes are not interpreted, and the fields alias line.
func SplitFields(line []byte, sep byte, dst [][]byte) [][]byte {
	out := dst[:0]
	rest := line
	for {
		i := bytes.IndexByte(rest, sep)
		if i < 0 {
			return append(out, rest)
		}
		out = append(out, rest[:i])
		rest = rest[i+1:]
	}
}
