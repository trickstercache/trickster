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

package static

import "testing"

func TestTypeByExtension(t *testing.T) {
	overrides := map[string]string{".html": "text/x-override", ".custom": "text/x-custom"}
	tests := []struct {
		name, expected string
	}{
		{"index.html", "text/x-override"},
		{"page.custom", "text/x-custom"},
		{"APP.JS", "text/javascript; charset=utf-8"},
		{"a/b/font.woff2", "font/woff2"},
		{"site.webmanifest", "application/manifest+json"},
		{"README", ""},
		{"file.unknown-extension", ""},
	}
	for _, test := range tests {
		if ct := typeByExtension(test.name, overrides); ct != test.expected {
			t.Errorf("%s: expected %q got %q", test.name, test.expected, ct)
		}
	}
}

func TestSniffType(t *testing.T) {
	if ct := sniffType(nil); ct != contentTypeOctetStream {
		t.Errorf("expected %q for no content, got %q", contentTypeOctetStream, ct)
	}
	long := make([]byte, sniffLen*2)
	copy(long, "%PDF-")
	if ct := sniffType(long); ct != "application/pdf" {
		t.Errorf("expected application/pdf, got %q", ct)
	}
}

func TestBaseType(t *testing.T) {
	if bt := baseType("text/html; charset=utf-8"); bt != "text/html" {
		t.Errorf("expected text/html, got %q", bt)
	}
	if bt := baseType("image/png"); bt != "image/png" {
		t.Errorf("expected image/png, got %q", bt)
	}
}
