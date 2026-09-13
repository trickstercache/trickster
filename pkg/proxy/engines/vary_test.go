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

package engines

import (
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func varyHeader(lines ...string) http.Header {
	h := http.Header{}
	for _, l := range lines {
		h.Add(headers.NameVary, l)
	}
	return h
}

func TestVaryFieldNames(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		expected  []string
		matchable bool
	}{
		{"absent", nil, nil, true},
		{"single", []string{"X-Mecone-Select"}, []string{"X-Mecone-Select"}, true},
		{
			"comma list",
			[]string{"X-Mecone-Color, X-Mecone-Shape"},
			[]string{"X-Mecone-Color", "X-Mecone-Shape"},
			true,
		},
		// RFC 9110 5.3: every field line nominates
		{
			"repeated lines",
			[]string{"X-Mecone-Color", "X-Mecone-Shape"},
			[]string{"X-Mecone-Color", "X-Mecone-Shape"},
			true,
		},
		{"case is normalized", []string{"x-mecone-select"}, []string{"X-Mecone-Select"}, true},
		{
			"order does not matter",
			[]string{"X-Mecone-Shape, X-Mecone-Color"},
			[]string{"X-Mecone-Color", "X-Mecone-Shape"},
			true,
		},
		{"duplicates collapse", []string{"X-A, X-A"}, []string{"X-A"}, true},
		{"empty entries ignored", []string{"X-A, , X-B"}, []string{"X-A", "X-B"}, true},
		{"asterisk is unmatchable", []string{"*"}, nil, false},
		{"asterisk among others", []string{"X-A, *"}, nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, matchable := varyFieldNames(varyHeader(test.lines...))
			if matchable != test.matchable {
				t.Fatalf("matchable got %t want %t", matchable, test.matchable)
			}
			if !slices.Equal(got, test.expected) {
				t.Errorf("got %v want %v", got, test.expected)
			}
		})
	}
}

func TestVarySecondaryKey(t *testing.T) {
	names := []string{"X-Mecone-Select"}
	key := func(h http.Header) string { return varySecondaryKey("primary", "gen1", names, h) }

	withValue := http.Header{"X-Mecone-Select": []string{"alpha"}}
	same := http.Header{"X-Mecone-Select": []string{"alpha"}}
	other := http.Header{"X-Mecone-Select": []string{"beta"}}
	absent := http.Header{}
	empty := http.Header{"X-Mecone-Select": []string{""}}

	if key(withValue) != key(same) {
		t.Error("expected equal values to produce one key")
	}
	if key(withValue) == key(other) {
		t.Error("expected differing values to produce different keys")
	}
	// a field the request omitted must not match one it sent, even empty
	if key(absent) == key(empty) {
		t.Error("expected absent and empty to differ")
	}
	if key(absent) == key(withValue) {
		t.Error("expected absent and present to differ")
	}
	// no combination of repeated values may masquerade as another
	multi := http.Header{"X-Mecone-Select": []string{"a", "b"}}
	joined := http.Header{"X-Mecone-Select": []string{"a, b"}}
	if key(multi) == key(joined) {
		t.Error("expected repeated values to differ from one joined value")
	}
	// the key stays under the primary key's namespace
	if got := key(withValue); len(got) <= len("primary") || got[:len("primary")] != "primary" {
		t.Errorf("expected a key derived from the primary, got %s", got)
	}
	// a new generation retires every variant of the URI at once, which is what
	// makes invalidation reach variants whose own entries outlive their index
	if varySecondaryKey("primary", "gen2", names, withValue) == key(withValue) {
		t.Error("expected a new generation to produce different variant keys")
	}
}

func TestNewVaryGeneration(t *testing.T) {
	a, b := newVaryGeneration(), newVaryGeneration()
	if a == "" || a == b {
		t.Errorf("expected distinct non-empty generations, got %q and %q", a, b)
	}
}

func TestSetVaryNames(t *testing.T) {
	pr := &proxyRequest{
		Request:    &http.Request{Header: http.Header{"X-A": []string{"1"}}},
		primaryKey: "primary",
		key:        "primary",
	}
	// with nothing nominated the object lives under the primary key
	pr.setVaryNames(nil)
	if pr.key != "primary" {
		t.Errorf("got %s expected the primary key", pr.key)
	}
	pr.setVaryNames([]string{"X-A"})
	if pr.key == "primary" {
		t.Error("expected a secondary key")
	}
	first := pr.key
	pr.Header.Set("X-A", "2")
	pr.setVaryNames([]string{"X-A"})
	if pr.key == first {
		t.Error("expected a different value to select a different variant")
	}
}

// ShallowCopy is written out field by field, so a field added to HTTPDocument
// is silently dropped on every memory-cache read until it is listed there.
// That is how VaryNames first went missing.
func TestShallowCopyCoversEveryExportedField(t *testing.T) {
	src := &HTTPDocument{
		IsMeta:        true,
		IsChunk:       true,
		StatusCode:    204,
		Status:        "a status",
		Headers:       map[string][]string{"X-A": {"1"}},
		Body:          []byte("body"),
		ContentLength: 4,
		ContentType:   "text/plain",
		CachingPolicy: &CachingPolicy{ETag: "etag"},
		VaryNames:     []string{"X-A"},
	}
	dst := src.ShallowCopy()

	sv, dv := reflect.ValueOf(src).Elem(), reflect.ValueOf(dst).Elem()
	for i := range sv.NumField() {
		f := sv.Type().Field(i)
		if !f.IsExported() || sv.Field(i).IsZero() {
			continue
		}
		if dv.Field(i).IsZero() {
			t.Errorf("ShallowCopy drops %s", f.Name)
		}
	}
}
