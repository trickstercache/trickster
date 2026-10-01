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

// Package codecpool lends streaming encoders and decoders from pools, so that
// a response reuses the state of a codec instead of building its own
package codecpool

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"
)

// ErrClosed is returned by a codec that is used after it was closed
var ErrClosed = errors.New("codec used after close")

// Encoder is a streaming encoder that can be pointed at a new destination
type Encoder interface {
	io.WriteCloser
	Flush() error
	Reset(io.Writer)
}

// Decoder is a streaming decoder that can be pointed at a new source
type Decoder interface {
	io.Reader
	Reset(io.Reader) error
}

// Encoders lends Encoders of one kind and configuration
type Encoders struct {
	pool sync.Pool
}

// NewEncoders returns an Encoders that builds a new Encoder with fn when it
// has none to lend
func NewEncoders(fn func() Encoder) *Encoders {
	e := &Encoders{}
	e.pool.New = func() any { return fn() }
	return e
}

// Get returns an encoder writing to w, which returns to the pool when closed
func (e *Encoders) Get(w io.Writer) io.WriteCloser {
	enc := e.pool.Get().(Encoder)
	enc.Reset(w)
	return &pooledEncoder{enc: enc, from: e}
}

type pooledEncoder struct {
	enc    Encoder
	from   *Encoders
	closed atomic.Bool
}

func (p *pooledEncoder) Write(b []byte) (int, error) {
	if p.enc == nil {
		return 0, ErrClosed
	}
	return p.enc.Write(b)
}

func (p *pooledEncoder) Flush() error {
	if p.enc == nil {
		return ErrClosed
	}
	return p.enc.Flush()
}

func (p *pooledEncoder) ReadFrom(r io.Reader) (int64, error) {
	if p.enc == nil {
		return 0, ErrClosed
	}
	if rf, ok := p.enc.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return tbytes.Copy(writerOnly{p.enc}, r)
}

// only the first Close returns the encoder, so that it is never lent to two users at once
func (p *pooledEncoder) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	enc := p.enc
	p.enc = nil
	err := enc.Close()
	// the pooled encoder must not keep the destination reachable
	enc.Reset(nil)
	p.from.pool.Put(enc)
	return err
}

// Decoders lends Decoders of one kind and configuration
type Decoders struct {
	pool sync.Pool
}

// NewDecoders returns a Decoders that builds a new Decoder with fn when it
// has none to lend
func NewDecoders(fn func() Decoder) *Decoders {
	d := &Decoders{}
	d.pool.New = func() any { return fn() }
	return d
}

// Get returns a decoder reading from r, which returns to the pool when closed.
// A source whose stream header can't be read yields a decoder whose reads fail.
func (d *Decoders) Get(r io.Reader) reader.ReadCloserResetter {
	dec := d.pool.Get().(Decoder)
	return &pooledDecoder{dec: dec, from: d, err: dec.Reset(r)}
}

type pooledDecoder struct {
	dec    Decoder
	from   *Decoders
	err    error
	closed atomic.Bool
}

func (p *pooledDecoder) Read(b []byte) (int, error) {
	switch {
	case p.dec == nil:
		return 0, ErrClosed
	case p.err != nil:
		return 0, p.err
	}
	return p.dec.Read(b)
}

func (p *pooledDecoder) WriteTo(w io.Writer) (int64, error) {
	switch {
	case p.dec == nil:
		return 0, ErrClosed
	case p.err != nil:
		return 0, p.err
	}
	if wt, ok := p.dec.(io.WriterTo); ok {
		return wt.WriteTo(w)
	}
	return tbytes.Copy(w, readerOnly{p.dec})
}

func (p *pooledDecoder) Reset(r io.Reader) error {
	if p.dec == nil {
		return ErrClosed
	}
	p.err = p.dec.Reset(r)
	return p.err
}

// only the first Close returns the decoder, so that it is never lent to two users at once
func (p *pooledDecoder) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	dec := p.dec
	p.dec = nil
	// the pooled decoder must not keep the source reachable; an empty source fails any
	// stream header read harmlessly, where a nil one can panic
	_ = dec.Reset(emptySource{})
	p.from.pool.Put(dec)
	return nil
}

type emptySource struct{}

func (emptySource) Read([]byte) (int, error) {
	return 0, io.EOF
}

// these hide a codec's own ReaderFrom and WriterTo, so a fallback copy doesn't call back into them
type writerOnly struct {
	io.Writer
}

type readerOnly struct {
	io.Reader
}
