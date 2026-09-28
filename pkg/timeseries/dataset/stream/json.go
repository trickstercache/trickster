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
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

var (
	// ErrNull is returned by Object and Array for a JSON null, which is consumed,
	// so callers that accept a null may continue.
	ErrNull = fmt.Errorf("%w: unexpected null JSON value", timeseries.ErrInvalidBody)
	// ErrUnexpectedToken indicates a JSON value was not the expected object or array.
	ErrUnexpectedToken = fmt.Errorf("%w: unexpected JSON token", timeseries.ErrInvalidBody)
	// ErrTrailingData indicates input remained after the walk returned.
	ErrTrailingData = fmt.Errorf("%w: unconsumed JSON input after document", timeseries.ErrInvalidBody)
	// ErrValueNotConsumed indicates an Object or Array visitor returned without
	// consuming the value it was called for.
	ErrValueNotConsumed = errors.New("visitor did not consume a JSON value")
)

// JSON is a Decoder for one JSON document that is walked token by token, so
// only the current token or decoded element is held in memory.
type JSON struct {
	walk   func(dec *json.Decoder) error
	finish FinishFunc
	buf    []byte
	read   bool
	err    error
	done   bool
}

var _ Decoder = (*JSON)(nil)

// NewJSON returns a JSON decoder. walk must consume exactly one JSON value
// from dec, which has UseNumber set, and finish is called by Finish afterward.
func NewJSON(walk func(dec *json.Decoder) error, finish FinishFunc) *JSON {
	return &JSON{walk: walk, finish: finish}
}

// Write buffers p until ReadFrom or Finish walks the document. Use ReadFrom to
// decode while the input is being read.
func (j *JSON) Write(p []byte) (int, error) {
	if err := j.check(); err != nil {
		return 0, err
	}
	j.buf = append(j.buf, p...)
	return len(p), nil
}

// ReadFrom walks the document from any previously written bytes followed by r,
// reading r to EOF. No input may be provided afterward.
func (j *JSON) ReadFrom(r io.Reader) (int64, error) {
	if err := j.check(); err != nil {
		return 0, err
	}
	j.read = true
	cr := &countingReader{r: r}
	var src io.Reader = cr
	if len(j.buf) > 0 {
		src = io.MultiReader(bytes.NewReader(j.buf), cr)
	}
	err := j.run(src)
	j.buf = nil
	if err != nil {
		j.err = err
	}
	return cr.n, err
}

// Finish walks any buffered input not yet walked, then calls the finish function.
func (j *JSON) Finish() (timeseries.Timeseries, error) {
	if j.done {
		return nil, ErrFinished
	}
	j.done = true
	if j.err != nil {
		return nil, j.err
	}
	if !j.read {
		err := j.run(bytes.NewReader(j.buf))
		j.buf = nil
		if err != nil {
			j.err = err
			return nil, err
		}
	}
	return j.finish()
}

func (j *JSON) check() error {
	switch {
	case j.done:
		return ErrFinished
	case j.err != nil:
		return j.err
	case j.read:
		return ErrInputConsumed
	}
	return nil
}

func (j *JSON) run(r io.Reader) error {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	err := j.walk(dec)
	if err == nil {
		// only whitespace may follow the document
		_, err = dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err == nil {
			return ErrTrailingData
		}
	}
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Object consumes a JSON object from dec, calling fn with each key in arrival
// order. fn must consume the key's value, e.g. with dec.Decode, Object, Array or Skip.
func Object(dec *json.Decoder, fn func(key string) error) error {
	if err := open(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return ErrUnexpectedToken
		}
		offset := dec.InputOffset()
		if err := fn(key); err != nil {
			return err
		}
		if dec.InputOffset() == offset {
			return ErrValueNotConsumed
		}
	}
	return closeDelim(dec, '}')
}

// Array consumes a JSON array from dec, calling fn once per element. fn must
// consume the element, e.g. with dec.Decode, Object, Array or Skip.
func Array(dec *json.Decoder, fn func() error) error {
	if err := open(dec, '['); err != nil {
		return err
	}
	for dec.More() {
		offset := dec.InputOffset()
		if err := fn(); err != nil {
			return err
		}
		if dec.InputOffset() == offset {
			return ErrValueNotConsumed
		}
	}
	return closeDelim(dec, ']')
}

// Skip consumes and discards the next JSON value from dec one token at a time, so a
// large skipped value is never held in memory. It fails if no value comes next.
func Skip(dec *json.Decoder) error {
	var depth int
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('['), json.Delim('{'):
			depth++
		case json.Delim(']'), json.Delim('}'):
			depth--
		}
		switch {
		case depth < 0:
			return ErrUnexpectedToken
		case depth == 0:
			return nil
		}
	}
}

func open(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return ErrNull
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return ErrUnexpectedToken
	}
	return nil
}

func closeDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return ErrUnexpectedToken
	}
	return nil
}

// JSONTagString is a BuilderOptions.TagString for JSON input: it unquotes JSON
// strings and keeps other literals, such as numbers, as their raw text.
func JSONTagString(_ timeseries.FieldDefinition, raw []byte) string {
	if isQuoted(raw) {
		if b, err := unquote(raw); err == nil {
			return string(b)
		}
	}
	return string(raw)
}
