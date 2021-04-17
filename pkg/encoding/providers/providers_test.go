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

package providers

import "testing"

func TestString(t *testing.T) {
	var p Provider = 8
	if p.String() != "deflate" {
		t.Error("expected 'deflate' got", p.String())
	}
	p = 9
	if p.String() != "9" {
		t.Error("expected '9' got", p.String())
	}
}

func TestWebProviders(t *testing.T) {
	p := WebProviders()
	if len(p) != 4 {
		t.Errorf("expected %d got %d", 4, len(p))
	}
}

func TestProviders(t *testing.T) {
	p := Providers()
	if len(p) != 5 {
		t.Errorf("expected %d got %d", 5, len(p))
	}
}

func TestProviderID(t *testing.T) {
	p := ProviderID("gzip")
	if p != GZip {
		t.Errorf("expected %d got %d", GZip, p)
	}
	p = ProviderID("invalid")
	if p != 0 {
		t.Errorf("expected %d got %d", 0, p)
	}
}

func TestCloneLookup(t *testing.T) {
	l := Lookup{GZipValue: GZip}
	l2 := l.Clone()
	if l2[GZipValue] != GZip {
		t.Error("clone mismatch")
	}
}

func TestGetCompatibleWebProviders(t *testing.T) {

	s, p := GetCompatibleWebProviders("")
	if s != "" {
		t.Error("got", s)
	}
	if p != 0 {
		t.Error("got", p)
	}

	s, p = GetCompatibleWebProviders("unsupported")
	if s != "" {
		t.Error("got", s)
	}
	if p != 0 {
		t.Error("got", p)
	}

	s, p = GetCompatibleWebProviders("br")
	if s != BrotliValue {
		t.Error("got", s)
	}
	if p != Brotli {
		t.Error("got", p)
	}

}
