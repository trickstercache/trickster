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

package profile

import (
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func TestClone(t *testing.T) {
	p := &Profile{Supported: 4}
	p2 := p.Clone()
	if p2.Supported != 4 {
		t.Error("mismatch")
	}
}

func TestString(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		p := &Profile{
			ClientAcceptEncoding: "test-ae",
			Supported:            4,
			SupportedHeaderVal:   "test-ae-header",
			NoTransform:          true,
			ContentEncoding:      "gzip",
			CompressTypes:        sets.New([]string{headers.ValueTextPlain}),
			ContentType:          headers.ValueTextPlain,
		}
		s := p.String()
		if !strings.Contains(s, headers.ValueTextPlain) {
			t.Error("mismatch")
		}
	})

	t.Run("with level", func(t *testing.T) {
		p := &Profile{
			ClientAcceptEncoding: "test-ae",
			Supported:            4,
			SupportedHeaderVal:   "test-ae-header",
			NoTransform:          true,
			ContentEncoding:      "gzip",
			CompressTypes:        sets.New([]string{headers.ValueTextPlain}),
			ContentType:          headers.ValueTextPlain,
			Level:                3,
		}
		s := p.String()
		if !strings.Contains(s, `"level":"3"`) {
			t.Error("mismatch")
		}
	})
}

func TestClientAcceptsEncoding(t *testing.T) {
	p := &Profile{Supported: 1}
	b := p.ClientAcceptsEncoding(1)
	if !b {
		t.Error("expected true")
	}
	b = p.ClientAcceptsEncoding(2)
	if b {
		t.Error("expected false")
	}
}

func TestGetEncoderInitializer(t *testing.T) {
	p := &Profile{}
	f, s := p.GetEncoderInitializer()
	if f != nil {
		t.Error("expected nil")
	}
	if s != "" {
		t.Error("expected empty string, got", s)
	}

	p.Supported = 8
	f, s = p.GetEncoderInitializer()
	if f != nil {
		t.Error("expected nil")
	}
	if s != "" {
		t.Error("expected empty string, got", s)
	}

	p.ContentType = "text/plain; charset=utf-8"
	f, s = p.GetEncoderInitializer()
	if f != nil {
		t.Error("expected nil")
	}
	if s != "" {
		t.Error("expected empty string, got", s)
	}

	p.CompressTypes = sets.New([]string{headers.ValueTextPlain})
	f, s = p.GetEncoderInitializer()
	if f == nil {
		t.Error("expected non-nil")
	}
	if s != "deflate" {
		t.Error("expected deflate string, got", s)
	}

	p.Supported = 4 // gzip
	f, s = p.GetEncoderInitializer()
	if f == nil {
		t.Error("expected non-nil")
	}
	if s != "gzip" {
		t.Error("expected deflate string, got", s)
	}

	p.Supported = 2 // br
	f, s = p.GetEncoderInitializer()
	if f == nil {
		t.Error("expected non-nil")
	}
	if s != "br" {
		t.Error("expected gzip string, got", s)
	}

	p.Supported = 1 // zstd
	f, s = p.GetEncoderInitializer()
	if f == nil {
		t.Error("expected non-nil")
	}
	if s != "zstd" {
		t.Error("expected br string, got", s)
	}
}

func TestGetDecoderInitializer(t *testing.T) {
	p := &Profile{}
	f := p.GetDecoderInitializer()
	if f != nil {
		t.Error("expected nil function")
	}
}

func TestGetEncoderInitializerHonorsClientPreference(t *testing.T) {
	p := &Profile{
		ContentType:   "text/plain",
		CompressTypes: sets.New([]string{"text/plain"}),
		Accepted:      providers.ParseAcceptEncoding("zstd;q=0.2, gzip;q=0.9, br;q=0.5"),
	}
	p.Supported = p.Accepted.Bitmap()
	if _, name := p.GetEncoderInitializer(); name != providers.GZipValue {
		t.Errorf("expected the client's most preferred encoding, got %q", name)
	}
	// narrowed since the header was read, the next most preferred that remains
	p.Supported &^= providers.GZip
	if _, name := p.GetEncoderInitializer(); name != providers.BrotliValue {
		t.Errorf("expected the most preferred encoding still supported, got %q", name)
	}
	p.Supported = 0
	if ei, _ := p.GetEncoderInitializer(); ei != nil {
		t.Error("expected no encoder when nothing is supported")
	}
	// a profile built without a client's header keeps Trickster's own preference
	p.Accepted, p.Supported = providers.Accepted{}, providers.GZip|providers.Brotli
	if _, name := p.GetEncoderInitializer(); name != providers.BrotliValue {
		t.Errorf("expected Trickster's preference without a client's, got %q", name)
	}
}
