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
	"bytes"
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"
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

type responseEncoder struct {
	prepared            bool
	http.ResponseWriter // the writer that is sent through the compressor
	EncodingProfile     *profile.Profile
	encoder             io.WriteCloser
	decoder             reader.ReadCloserResetter
	buff                *bytes.Buffer
	writeFunc           writeFunc
	decoderInit         providers.DecoderInitializer
}

// Write implements ResponseEncoder.Write
func (ew *responseEncoder) Write(b []byte) (int, error) {
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
	if ew.encoder == nil && ew.decoderInit != nil {
		ew.writeFunc = ew.writeDecoded
		return
	}
	if ew.encoder != nil && ew.decoderInit == nil {
		ew.writeFunc = ew.writeEncoded
		return
	}
	if ew.encoder != nil && ew.decoderInit != nil {
		ew.writeFunc = ew.writeTranscoded
		return
	}
	ew.writeFunc = ew.writeDirect
}

func (ew *responseEncoder) writeDirect(b []byte) (int, error) {
	return ew.ResponseWriter.Write(b)
}

func (ew *responseEncoder) writeEncoded(b []byte) (int, error) {
	_, err := ew.encoder.Write(b)
	return len(b), err
}

func (ew *responseEncoder) writeDecoded(b []byte) (int, error) {
	if ew.buff == nil {
		ew.buff = bytes.NewBuffer(b)
		ew.decoder = ew.decoderInit(io.NopCloser(ew.buff)) // new readcloser for bytes to go in
	} else {
		err := ew.decoder.Reset(reader.NewReadCloserResetterBytes(b))
		if err != nil {
			return 0, err
		}
	}
	_, err := io.Copy(ew.ResponseWriter, ew.decoder)
	return len(b), err
}

func (ew *responseEncoder) writeTranscoded(b []byte) (int, error) {
	if ew.buff == nil {
		ew.buff = bytes.NewBuffer(b)
		ew.decoder = ew.decoderInit(io.NopCloser(ew.buff)) // new readcloser for bytes to go in
	} else {
		err := ew.decoder.Reset(reader.NewReadCloserResetterBytes(b))
		if err != nil {
			return 0, err
		}
	}
	_, err := io.Copy(ew.encoder, ew.decoder)
	return len(b), err
}

func (ew *responseEncoder) Close() error {
	var err1, err2 error
	if ew.encoder != nil {
		err1 = ew.encoder.Close()
	}
	if ew.decoder != nil {
		err2 = ew.decoder.Close()
	}
	if err1 != nil {
		return err1
	}
	return err2
}
