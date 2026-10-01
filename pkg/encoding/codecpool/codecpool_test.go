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

package codecpool

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// an encoder that upper-cases what it is given, and counts what is done to it
type fakeEncoder struct {
	w                       io.Writer
	resets, closes, flushes int
}

func (f *fakeEncoder) Write(b []byte) (int, error) {
	return f.w.Write(bytes.ToUpper(b))
}

func (f *fakeEncoder) Flush() error {
	f.flushes++
	return nil
}

func (f *fakeEncoder) Close() error {
	f.closes++
	return nil
}

func (f *fakeEncoder) Reset(w io.Writer) {
	f.resets++
	f.w = w
}

// a decoder whose stream header is a leading '+'
type fakeDecoder struct {
	r io.Reader
}

var errBadHeader = errors.New("bad header")

func (f *fakeDecoder) Reset(r io.Reader) error {
	f.r = r
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return err
	}
	if b[0] != '+' {
		return errBadHeader
	}
	return nil
}

func (f *fakeDecoder) Read(b []byte) (int, error) {
	return f.r.Read(b)
}

func TestEncoders(t *testing.T) {
	var built int
	encs := NewEncoders(func() Encoder {
		built++
		return &fakeEncoder{}
	})
	var buf bytes.Buffer
	w := encs.Get(&buf)
	_, err := w.Write([]byte("trickster"))
	require.NoError(t, err)
	require.NoError(t, w.(interface{ Flush() error }).Flush())
	n, err := w.(io.ReaderFrom).ReadFrom(strings.NewReader(" cache"))
	require.NoError(t, err)
	require.EqualValues(t, 6, n)
	require.Equal(t, "TRICKSTER CACHE", buf.String())

	enc := w.(*pooledEncoder).enc.(*fakeEncoder)
	require.NoError(t, w.Close())
	require.NoError(t, w.Close())
	require.Equal(t, 1, enc.closes)
	require.Nil(t, enc.w)

	_, err = w.Write([]byte("x"))
	require.ErrorIs(t, err, ErrClosed)
	require.ErrorIs(t, w.(interface{ Flush() error }).Flush(), ErrClosed)
	_, err = w.(io.ReaderFrom).ReadFrom(strings.NewReader("x"))
	require.ErrorIs(t, err, ErrClosed)

	// the encoder was returned once, so no two later Gets can both have it
	a, b := encs.Get(io.Discard), encs.Get(io.Discard)
	require.NotSame(t, a.(*pooledEncoder).enc, b.(*pooledEncoder).enc)
	require.Positive(t, built)
}

type readerFromEncoder struct {
	fakeEncoder
	readFrom bool
}

func (r *readerFromEncoder) ReadFrom(src io.Reader) (int64, error) {
	r.readFrom = true
	return io.Copy(r.w, src)
}

func TestEncodersReadFromDelegates(t *testing.T) {
	enc := &readerFromEncoder{}
	encs := NewEncoders(func() Encoder { return enc })
	var buf bytes.Buffer
	w := encs.Get(&buf)
	// a source without WriterTo, so that the copy asks the destination to read from it
	_, err := io.Copy(w, io.LimitReader(strings.NewReader("abc"), 3))
	require.NoError(t, err)
	require.True(t, enc.readFrom)
	require.Equal(t, "abc", buf.String())
}

func TestDecoders(t *testing.T) {
	decs := NewDecoders(func() Decoder { return &fakeDecoder{} })
	r := decs.Get(strings.NewReader("+trickster"))
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "trickster", string(b))

	require.NoError(t, r.Reset(strings.NewReader("+again")))
	var buf bytes.Buffer
	_, err = r.(io.WriterTo).WriteTo(&buf)
	require.NoError(t, err)
	require.Equal(t, "again", buf.String())

	dec := r.(*pooledDecoder).dec.(*fakeDecoder)
	require.NoError(t, r.Close())
	require.NoError(t, r.Close())
	require.Equal(t, emptySource{}, dec.r)

	_, err = r.Read(make([]byte, 1))
	require.ErrorIs(t, err, ErrClosed)
	_, err = r.(io.WriterTo).WriteTo(io.Discard)
	require.ErrorIs(t, err, ErrClosed)
	require.ErrorIs(t, r.Reset(strings.NewReader("x")), ErrClosed)
}

func TestDecodersHeaderError(t *testing.T) {
	decs := NewDecoders(func() Decoder { return &fakeDecoder{} })
	r := decs.Get(strings.NewReader("!bad"))
	_, err := r.Read(make([]byte, 4))
	require.ErrorIs(t, err, errBadHeader)
	_, err = r.(io.WriterTo).WriteTo(io.Discard)
	require.ErrorIs(t, err, errBadHeader)
	// a good source recovers the decoder
	require.NoError(t, r.Reset(strings.NewReader("+ok")))
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "ok", string(b))
	require.NoError(t, r.Close())
}

func TestEmptySource(t *testing.T) {
	n, err := emptySource{}.Read(make([]byte, 1))
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
}
