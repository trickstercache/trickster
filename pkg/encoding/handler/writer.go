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

package handler

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"sync"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// ResponseEncoder defines the ResponseEncoder interface for encoding responses
// just-in-time
type ResponseEncoder interface {
	Write([]byte) (int, error)
	Header() http.Header
	WriteHeader(int)
	Close() error
}

// NewEncoder returns a new ResponseEncoder
func NewEncoder(w http.ResponseWriter, ep *profile.Profile) ResponseEncoder {
	if ep == nil {
		ep = &profile.Profile{Level: -1}
	}
	return &responseEncoder{
		ResponseWriter:  w,
		EncodingProfile: ep,
	}
}

type writeFunc func([]byte) (int, error)

// a held body larger than this is dropped after use, so that it doesn't keep its memory
const maxPooledHeldBuffer = 1 << 20

var heldBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func putHeldBuffer(buf *bytes.Buffer) {
	if buf.Cap() <= maxPooledHeldBuffer {
		buf.Reset()
		heldBuffers.Put(buf)
	}
}

type responseEncoder struct {
	prepared            bool
	http.ResponseWriter // the writer that is sent through the compressor
	EncodingProfile     *profile.Profile
	encoder             io.WriteCloser
	// an encoded body the client can't take is held here, and decoded whole at Close
	held        *bytes.Buffer
	writeFunc   writeFunc
	decoderInit providers.DecoderInitializer
	hijacked    bool
}

var _ http.Hijacker = (*responseEncoder)(nil)

// Write implements ResponseEncoder.Write
func (ew *responseEncoder) Write(b []byte) (int, error) {
	if ew.hijacked {
		return 0, http.ErrHijacked
	}
	if !ew.prepared {
		ew.prepareWriter()
	}
	return ew.writeFunc(b)
}

// WriteHeader implements ResponseEncoder.WriteHeader
func (ew *responseEncoder) WriteHeader(c int) {
	if !ew.prepared {
		ew.prepareWriter()
	}
	ew.ResponseWriter.WriteHeader(c)
}

// Header implements ResponseEncoder.Header
func (ew *responseEncoder) Header() http.Header {
	return ew.ResponseWriter.Header()
}

// FlushError flushes the active transform before the underlying writer, so a
// streaming response is not left buffered inside the compressor. Without it,
// http.ResponseController.Flush would follow Unwrap straight to the network.
func (ew *responseEncoder) FlushError() error {
	if ew.hijacked {
		return http.ErrHijacked
	}
	if ew.encoder != nil {
		if f, ok := ew.encoder.(interface{ Flush() error }); ok {
			if err := f.Flush(); err != nil {
				return err
			}
		}
	}
	return http.NewResponseController(ew.ResponseWriter).Flush()
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (ew *responseEncoder) Unwrap() http.ResponseWriter {
	return ew.ResponseWriter
}

// Hijack delegates connection ownership to the underlying response writer.
func (ew *responseEncoder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(ew.ResponseWriter).Hijack()
	if err == nil {
		ew.hijacked = true
	}
	return c, rw, err
}

func (ew *responseEncoder) prepareWriter() {
	ep := ew.EncodingProfile
	h := ew.Header()
	if ep == nil {
		ew.EncodingProfile = &profile.Profile{
			ContentEncoding: h.Get(headers.NameContentEncoding),
		}
		ep = ew.EncodingProfile
	}
	ep.ContentType = h.Get(headers.NameContentType)

	if ep.ContentEncoding == "" { // content from origin is not encoded
		ei, en := ep.GetEncoderInitializer()
		if ei != nil { // the client will allow this response to be encoded by trickster
			ew.encoder = ei(ew.ResponseWriter, ep.Level)
			h.Del(headers.NameContentLength)
			h.Set(headers.NameContentEncoding, en)
		}
		// content is already encoded, check if trickster supports the provided encoding
	} else if ep.ContentEncodingNum = providers.ProviderID(ep.ContentEncoding); ep.ContentEncodingNum > 0 {
		// trickster supports the encoding. now check if the client supports it.
		if !ep.ClientAcceptsEncoding(ep.ContentEncodingNum) {
			// Client does not accept the encoding, so trickster will decode it on-the-fly
			ew.decoderInit = ep.GetDecoderInitializer()
			if ew.decoderInit != nil {
				h.Del(headers.NameContentEncoding)
				h.Del(headers.NameContentLength)
				// if the client accepts some kind of supported encoding, wire up the encoder
				ei, en := ep.GetEncoderInitializer()
				if ei != nil {
					ew.encoder = ei(ew.ResponseWriter, ep.Level)
					h.Set(headers.NameContentEncoding, en)
				}
			}
		}
	} // trickster doesn't support the encoding, so it is served as-is to client

	// this selects which WriterFunc is used for the request based on the combination of
	// nil vs non-nil encoder and decoders
	ew.selectWriter()
	ew.prepared = true
}

func (ew *responseEncoder) selectWriter() {
	switch {
	case ew.decoderInit != nil:
		ew.writeFunc = ew.writeHeld
	case ew.encoder != nil:
		ew.writeFunc = ew.writeEncoded
	default:
		ew.writeFunc = ew.writeDirect
	}
}

func (ew *responseEncoder) writeDirect(b []byte) (int, error) {
	return ew.ResponseWriter.Write(b)
}

func (ew *responseEncoder) writeEncoded(b []byte) (int, error) {
	_, err := ew.encoder.Write(b)
	return len(b), err
}

// a decoder given part of a stream fails where the part ends, and can't take up the rest, so
// an encoded body is held until Close
func (ew *responseEncoder) writeHeld(b []byte) (int, error) {
	if ew.held == nil {
		ew.held = heldBuffers.Get().(*bytes.Buffer)
	}
	return ew.held.Write(b)
}

// decodes the held body to the encoder if there is one, or else to the client
func (ew *responseEncoder) decodeHeld() error {
	held := ew.held
	ew.held = nil
	defer putHeldBuffer(held)
	var dest io.Writer = ew.ResponseWriter
	if ew.encoder != nil {
		dest = ew.encoder
	}
	// a reader, where the buffer itself would let a zstd decoder keep all it decodes
	dec := ew.decoderInit(bytes.NewReader(held.Bytes()))
	_, err := tbytes.Copy(dest, dec)
	dec.Close()
	return err
}

func (ew *responseEncoder) Close() error {
	if ew.hijacked {
		return nil
	}
	var err error
	if ew.held != nil {
		err = ew.decodeHeld()
	}
	if ew.encoder != nil {
		if cerr := ew.encoder.Close(); err == nil {
			err = cerr
		}
	}
	return err
}
