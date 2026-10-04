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

package stream

import (
	"bytes"
	"encoding/json/jsontext"
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

// randomJSON writes a random JSON value, with whitespace wherever the grammar allows it
func randomJSON(rng *weaktest.Rand, b *strings.Builder, depth int) {
	space := func() {
		b.WriteString([]string{"", "", " ", "\n\t", "\r\n  "}[rng.IntN(5)])
	}
	space()
	switch n := rng.IntN(9); {
	case depth < 3 && n == 0:
		b.WriteByte('[')
		for i := range rng.IntN(4) {
			if i > 0 {
				b.WriteByte(',')
			}
			randomJSON(rng, b, depth+1)
		}
		space()
		b.WriteByte(']')
	case depth < 3 && n == 1:
		b.WriteByte('{')
		for i := range rng.IntN(4) {
			if i > 0 {
				b.WriteByte(',')
			}
			space()
			b.WriteString(strconv.Quote("k" + strconv.Itoa(i) + []string{"", "\\\"]}", ",:[{"}[rng.IntN(3)]))
			space()
			b.WriteByte(':')
			randomJSON(rng, b, depth+1)
		}
		space()
		b.WriteByte('}')
	case n == 2:
		b.WriteString(`"a\"b\\],}{["`)
	case n == 3:
		b.WriteString(strconv.Quote(strings.Repeat("x", rng.IntN(4))))
	case n == 4:
		b.WriteString([]string{"true", "false", "null"}[rng.IntN(3)])
	case n == 5:
		b.WriteString("-1.5e+3")
	default:
		b.WriteString(strconv.Itoa(rng.IntN(1000)))
	}
	space()
}

// readElements reads raw's top level the way a jsontext.Decoder does, as the scanners must
func readElements(t *testing.T, raw []byte) (names, values []string) {
	t.Helper()
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.ReadToken()
	if err != nil {
		t.Fatal(err)
	}
	kind := tok.Kind()
	if kind != jsontext.KindBeginArray && kind != jsontext.KindBeginObject {
		return nil, nil
	}
	for {
		if k := dec.PeekKind(); k == jsontext.KindEndArray || k == jsontext.KindEndObject {
			return names, values
		}
		if kind == jsontext.KindBeginObject {
			name, err := dec.ReadValue()
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, string(name))
		}
		v, err := dec.ReadValue()
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, string(v))
	}
}

func checkScanners(t *testing.T, raw []byte) {
	t.Helper()
	wantNames, wantValues := readElements(t, raw)
	var names, values []string
	els := ArrayElements(raw)
	for v, ok := els.Next(); ok; v, ok = els.Next() {
		values = append(values, string(v))
	}
	members := ObjectMembers(raw)
	for name, v, ok := members.Next(); ok; name, v, ok = members.Next() {
		names, values = append(names, string(name)), append(values, string(v))
	}
	if strings.Join(names, "\x00") != strings.Join(wantNames, "\x00") ||
		strings.Join(values, "\x00") != strings.Join(wantValues, "\x00") {
		t.Fatalf("%s: got names %q values %q, want %q %q", raw, names, values, wantNames, wantValues)
	}
}

func TestRawScannersMatchJSONText(t *testing.T) {
	rng := weaktest.NewRand(11, 11)
	for range 20_000 {
		var b strings.Builder
		randomJSON(rng, &b, 0)
		raw, err := jsontext.NewDecoder(strings.NewReader(b.String())).ReadValue()
		if err != nil {
			t.Fatalf("%s: %v", b.String(), err)
		}
		checkScanners(t, raw)
	}
	// a value that isn't an array or object has no elements or members
	checkScanners(t, []byte(`"x"`))
	els, members := ArrayElements(nil), ObjectMembers(nil)
	if _, ok := els.Next(); ok {
		t.Error("no value has no elements")
	}
	if _, _, ok := members.Next(); ok {
		t.Error("no value has no members")
	}
}

func TestStringText(t *testing.T) {
	var buf []byte
	raw := []byte(`"plain"`)
	if got := StringText(raw, &buf); string(got) != "plain" || &got[0] != &raw[1] {
		t.Errorf("a plain string should be its own bytes: %q", got)
	}
	if got := StringText([]byte(`"a\"é"`), &buf); string(got) != "a\"é" {
		t.Errorf("got %q", got)
	}
}

func TestIsIntegerLiteral(t *testing.T) {
	for raw, want := range map[string]bool{"0": true, "-12": true, "1.5": false, "1e3": false, "2E-1": false} {
		if got := IsIntegerLiteral([]byte(raw)); got != want {
			t.Errorf("%s: got %t", raw, got)
		}
	}
}

func TestFieldName(t *testing.T) {
	for key, want := range map[string]string{"results": "results", "RESULTS": "results", "Error": "error", "x": ""} {
		if got := FieldName([]byte(key), "results", "error"); got != want {
			t.Errorf("%s: got %q, want %q", key, got, want)
		}
	}
	// an exact match wins over one that ignores case
	if got := FieldName([]byte("a"), "A", "a"); got != "a" {
		t.Errorf("got %q", got)
	}
}

func TestInterner(t *testing.T) {
	var in Interner
	a := in.String([]byte("name"), true)
	if b := in.Short([]byte("name")); b != a || in.m["name"] != a {
		t.Error("a shared string should be returned again")
	}
	long := bytes.Repeat([]byte("v"), maxInternedLen+1)
	in.Short(long)
	if _, ok := in.m[string(long)]; ok {
		t.Error("a long value should not be shared")
	}
	for i := range maxInterned + 10 {
		in.String([]byte(strconv.Itoa(i)), true)
	}
	if len(in.m) != maxInterned {
		t.Errorf("the interner holds %d strings, want at most %d", len(in.m), maxInterned)
	}
}
