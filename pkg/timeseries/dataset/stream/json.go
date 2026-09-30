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
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
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

// NewJSONDecoder returns a decoder of r like the one a JSON walk is given, which accepts a repeated
// object member and invalid UTF-8 as encoding/json does.
func NewJSONDecoder(r io.Reader) *jsontext.Decoder {
	return jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
}

// JSON is a Decoder for one JSON document that is walked token by token, so
// only the current token or value is held in memory.
type JSON struct {
	walk   func(dec *jsontext.Decoder) error
	finish FinishFunc
	buf    []byte
	read   bool
	err    error
	done   bool
}

var _ Decoder = (*JSON)(nil)

// NewJSON returns a JSON decoder. walk must consume exactly one JSON value from dec, which accepts a
// repeated object member and invalid UTF-8 as encoding/json does, and Finish then calls finish.
func NewJSON(walk func(dec *jsontext.Decoder) error, finish FinishFunc) *JSON {
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
	dec := NewJSONDecoder(r)
	err := j.walk(dec)
	if err == nil {
		// only whitespace may follow the document
		_, err = dec.ReadToken()
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
// order. fn must consume the key's value, e.g. with Decode, Object, Array, Skip or dec.ReadValue.
func Object(dec *jsontext.Decoder, fn func(key string) error) error {
	return ObjectBytes(dec, func(key []byte) error { return fn(string(key)) })
}

// ObjectBytes is Object with each key as its unescaped bytes, which are valid only until fn reads
// from dec, so a key compared or copied first costs no allocation.
func ObjectBytes(dec *jsontext.Decoder, fn func(key []byte) error) error {
	if err := open(dec, jsontext.KindBeginObject); err != nil {
		return err
	}
	depth := dec.StackDepth()
	var escaped []byte
	for {
		switch dec.PeekKind() {
		case jsontext.KindEndObject, jsontext.KindInvalid:
			_, err := dec.ReadToken()
			return err
		}
		name, err := dec.ReadValue()
		if err != nil {
			return err
		}
		var key []byte
		if n := len(name); n >= 2 && plainASCII(name[1:n-1]) {
			key = name[1 : n-1]
		} else {
			escaped = AppendString(escaped[:0], name)
			key = escaped
		}
		offset := dec.InputOffset()
		if err := fn(key); err != nil {
			return err
		}
		if err := consumed(dec, offset, depth); err != nil {
			return err
		}
	}
}

// Array consumes a JSON array from dec, calling fn once per element. fn must
// consume the element, e.g. with Decode, Object, Array, Skip or dec.ReadValue.
func Array(dec *jsontext.Decoder, fn func() error) error {
	if err := open(dec, jsontext.KindBeginArray); err != nil {
		return err
	}
	depth := dec.StackDepth()
	for {
		switch dec.PeekKind() {
		case jsontext.KindEndArray, jsontext.KindInvalid:
			_, err := dec.ReadToken()
			return err
		}
		offset := dec.InputOffset()
		if err := fn(); err != nil {
			return err
		}
		if err := consumed(dec, offset, depth); err != nil {
			return err
		}
	}
}

// consumed reports whether a visitor read exactly one whole value: some input, and back to depth
func consumed(dec *jsontext.Decoder, offset int64, depth int) error {
	switch {
	case dec.InputOffset() == offset:
		return ErrValueNotConsumed
	case dec.StackDepth() != depth:
		return ErrUnexpectedToken
	}
	return nil
}

// Skip consumes and discards the next JSON value from dec without holding it in
// memory. It fails if no value comes next.
func Skip(dec *jsontext.Decoder) error {
	switch dec.PeekKind() {
	case jsontext.KindEndArray, jsontext.KindEndObject:
		return ErrUnexpectedToken
	}
	return dec.SkipValue()
}

// Decode decodes the next JSON value from dec into v as encoding/json's Unmarshal
// would, with any opts, such as jsonv2.RejectUnknownMembers, applied after its defaults.
func Decode(dec *jsontext.Decoder, v any, opts ...jsonv2.Options) error {
	if len(opts) == 0 {
		return jsonv2.UnmarshalDecode(dec, v, json.DefaultOptionsV1())
	}
	return jsonv2.UnmarshalDecode(dec, v, jsonv2.JoinOptions(append([]jsonv2.Options{json.DefaultOptionsV1()}, opts...)...))
}

// AppendString appends the text of raw, a JSON string value as dec.ReadValue returns it, to dst.
// Invalid UTF-8 is written as U+FFFD, as encoding/json decodes it.
func AppendString(dst, raw []byte) []byte {
	if n := len(raw); n >= 2 && plainASCII(raw[1:n-1]) {
		return append(dst, raw[1:n-1]...)
	}
	// raw was validated as a JSON string by the decoder that read it, so the only error the
	// unquote can report is invalid UTF-8, which it has already replaced
	out, _ := jsontext.AppendUnquote(dst, raw)
	return out
}

// reports whether b holds only ASCII without escapes, which a JSON string's text is as written
func plainASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 || c == '\\' {
			return false
		}
	}
	return true
}

func open(dec *jsontext.Decoder, want jsontext.Kind) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	switch tok.Kind() {
	case want:
		return nil
	case jsontext.KindNull:
		return ErrNull
	}
	return ErrUnexpectedToken
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
