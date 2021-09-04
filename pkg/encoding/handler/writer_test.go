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
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/encoding/gzip"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/strings"
)

func TestNewEncoder(t *testing.T) {
	w := NewEncoder(nil, nil)
	if w.(*responseEncoder).EncodingProfile == nil {
		t.Error("expected non-nil")
	}
}

func TestWrite(t *testing.T) {

	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w}
	ew.writeFunc = ew.writeDirect

	ew.WriteHeader(200)
	ew.prepared = false

	i, err := ew.Write([]byte("trickster"))
	if i != 9 {
		t.Errorf("expected %d got %d", 9, i)
	}
	if err != nil {
		t.Error(err)
	}

	ew.encoder = &responseEncoder{ResponseWriter: w}
	ew.decoder = reader.NewReadCloserResetter(nil)
	ew.Close()

}

func TestSelectWriter(t *testing.T) {

	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w}
	ew.decoderInit = gzip.NewDecoder
	ew.selectWriter()
	if ew.writeFunc == nil {
		t.Error("expected non-nil")
	}

	ew.writeFunc = nil
	ew.encoder = &responseEncoder{ResponseWriter: w}
	ew.selectWriter()
	if ew.writeFunc == nil {
		t.Error("expected non-nil")
	}

	ew.writeFunc = nil
	ew.decoderInit = nil
	ew.selectWriter()
	if ew.writeFunc == nil {
		t.Error("expected non-nil")
	}

	ew.writeFunc = nil
	ew.encoder = nil
	ew.selectWriter()
	if ew.writeFunc == nil {
		t.Error("expected non-nil")
	}
}

func TestWriteEncoded(t *testing.T) {

	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w}
	ew2 := &responseEncoder{ResponseWriter: w}
	ew.encoder = ew2
	i, err := ew.writeEncoded([]byte("trickster"))
	if i != 9 {
		t.Errorf("expected %d got %d", 9, i)
	}
	if err != nil {
		t.Error(err)
	}
}

func TestWriteDecoded(t *testing.T) {

	const expected = "trickster"

	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w}
	ew.decoderInit = gzip.NewDecoder

	b, err := gzip.Encode([]byte(expected))
	if err != nil {
		t.Error(err)
	}
	i, err := ew.writeDecoded(b)
	if i != 33 {
		t.Errorf("expected %d got %d", 33, i)
	}
	if err != nil {
		t.Error(err)
	}
	s := w.Body.String()
	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

	// catch the reset clause
	w = httptest.NewRecorder()
	ew.ResponseWriter = w

	i, err = ew.writeDecoded(b)
	if i != 33 {
		t.Errorf("expected %d got %d", 33, i)
	}
	if err != nil {
		t.Error(err)
	}
	s = w.Body.String()
	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

}

func TestWriteTranscoded(t *testing.T) {

	const expected = "trickster"

	w := httptest.NewRecorder()
	ew := &responseEncoder{ResponseWriter: w}
	ew.decoderInit = gzip.NewDecoder
	ew.encoder = NewEncoder(w, nil)

	b, err := gzip.Encode([]byte(expected))
	if err != nil {
		t.Error(err)
	}
	i, err := ew.writeTranscoded(b)
	if i != 33 {
		t.Errorf("expected %d got %d", 33, i)
	}
	if err != nil {
		t.Error(err)
	}
	s := w.Body.String()
	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

	// catch the reset clause
	w = httptest.NewRecorder()
	ew.encoder = NewEncoder(w, nil)

	i, err = ew.writeTranscoded(b)
	if i != 33 {
		t.Errorf("expected %d got %d", 33, i)
	}
	if err != nil {
		t.Error(err)
	}
	s = w.Body.String()
	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

}

func TestPrepareWriter(t *testing.T) {

	w := httptest.NewRecorder()
	h := w.Header()
	ep := &profile.Profile{Supported: 1, ContentType: "text/plain",
		CompressTypes: strings.Lookup{"text/plain": nil}}
	ew := &responseEncoder{EncodingProfile: ep, ResponseWriter: w}
	h.Set(headers.NameContentType, "text/plain")
	ew.prepareWriter()
	if ew.encoder == nil {
		t.Error("expected non-nil encoder")
	}

	ep.ContentEncoding = "gzip"
	ep.ContentEncodingNum = 2
	ew.prepareWriter()
	if ew.encoder == nil {
		t.Error("expected non-nil encoder")
	}

}
