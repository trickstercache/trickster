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

	strutil "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

func TestClone(t *testing.T) {
	p := &Profile{Supported: 4}
	p2 := p.Clone()
	if p2.Supported != 4 {
		t.Error("mismatch")
	}
}

func TestString(t *testing.T) {
	p := &Profile{
		ClientAcceptEncoding: "test-ae",
		Supported:            4,
		SupportedHeaderVal:   "test-ae-header",
		NoTransform:          true,
		ContentEncoding:      "gzip",
		CompressTypes:        strutil.Lookup{"text/plain": nil},
		ContentType:          "text/plain",
	}
	s := p.String()
	if strings.Index(s, "text/plain") < 0 {
		t.Error("mismatch")
	}
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

	p.CompressTypes = map[string]interface{}{"text/plain": nil}
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
