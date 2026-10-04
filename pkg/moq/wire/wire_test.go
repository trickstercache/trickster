/*
 * Copyright 2026 The Trickster Authors
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

package wire

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"testing"
)

func TestReaderStickyErrors(t *testing.T) {
	r := NewReader([]byte{0x05, 0x01})
	if v := r.Varint(); v != 5 {
		t.Fatalf("got %d", v)
	}
	if r.Uint16() != 0 || !errors.Is(r.Err(), ErrShort) {
		t.Fatalf("a short Uint16 should fail with ErrShort, got %v", r.Err())
	}
	// every read after the first failure returns zero values and keeps the first error
	if r.Uint8() != 0 || r.Varint() != 0 || r.Bytes(1) != nil || r.Rest() != nil || r.Remaining() != 0 {
		t.Error("reads after a failure should return zero values")
	}
	r.Fail(ErrTrailing)
	if !errors.Is(r.Err(), ErrShort) {
		t.Error("the first error should be kept")
	}
}

func TestReaderPrimitives(t *testing.T) {
	b := AppendVarint(nil, 300)
	b = append(b, 0x07, 0x01, 0x02)
	b = AppendBytes(b, []byte("abc"))
	b = AppendString(b, "xy")
	b = append(b, 0xaa, 0xbb)
	r := NewReader(b)
	if r.Varint() != 300 || r.Uint8() != 7 || r.Uint16() != 0x0102 {
		t.Fatal("primitive mismatch")
	}
	if string(r.LenBytes(3)) != "abc" || string(r.LenBytes(10)) != "xy" {
		t.Fatal("length-prefixed bytes mismatch")
	}
	if r.Offset() != len(b)-2 || r.Remaining() != 2 {
		t.Fatalf("offset %d remaining %d", r.Offset(), r.Remaining())
	}
	if err := r.Done(); !errors.Is(err, ErrTrailing) {
		t.Errorf("Done with bytes left should fail with ErrTrailing, got %v", err)
	}
	r = NewReader(b[len(b)-2:])
	if rest := r.Rest(); len(rest) != 2 || r.Done() != nil {
		t.Error("Rest should consume everything")
	}

	r = NewReader(AppendBytes(nil, []byte("toolong")))
	if r.LenBytes(3) != nil || !errors.Is(r.Err(), ErrTooLong) {
		t.Errorf("a length past the limit should fail with ErrTooLong, got %v", r.Err())
	}
	r = NewReader([]byte{0x05, 0x00})
	if r.Count() != 0 || !errors.Is(r.Err(), ErrTooMany) {
		t.Errorf("a count larger than the input should fail with ErrTooMany, got %v", r.Err())
	}
	r = NewReader([]byte{0x80})
	if r.Count() != 0 || !errors.Is(r.Err(), ErrShort) {
		t.Error("a truncated count should fail")
	}
	r = NewReader([]byte{})
	if r.Uint8() != 0 || !errors.Is(r.Err(), ErrShort) {
		t.Error("an empty Uint8 should fail")
	}
	r = NewReader([]byte{0x01})
	if r.Bytes(2) != nil || !errors.Is(r.Err(), ErrShort) {
		t.Error("Bytes past the end should fail")
	}
	r = NewReader([]byte{0x80})
	if r.LenBytes(1) != nil || !errors.Is(r.Err(), ErrShort) {
		t.Error("LenBytes with a truncated length should fail")
	}
}

func TestLocation(t *testing.T) {
	a, b := Location{Group: 1, Object: 5}, Location{Group: 2, Object: 0}
	if !a.Less(b) || b.Less(a) || a.Compare(a) != 0 || b.Compare(a) != 1 {
		t.Error("ordering by group")
	}
	c := Location{Group: 1, Object: 6}
	if !a.Less(c) || c.Compare(a) != 1 {
		t.Error("ordering by object within a group")
	}
	r := NewReader(AppendLocation(nil, Location{Group: 1 << 40, Object: 3}))
	if got := r.Location(); got != (Location{Group: 1 << 40, Object: 3}) || r.Done() != nil {
		t.Errorf("round trip got %+v", got)
	}
}

func TestNamespace(t *testing.T) {
	ns := Namespace{[]byte("live"), []byte("sports")}
	b := AppendNamespace(nil, ns)
	b = AppendBytes(b, []byte("video"))
	r := NewReader(b)
	got := r.Namespace()
	name := r.TrackName(got)
	if !got.Equal(ns) || string(name) != "video" || r.Done() != nil {
		t.Fatalf("round trip got %q %q (%v)", got, name, r.Err())
	}
	if !ns.HasPrefix(Namespace{[]byte("live")}) || !ns.HasPrefix(Namespace{}) || ns.HasPrefix(Namespace{[]byte("liv")}) ||
		ns.HasPrefix(append(slices.Clone(ns), []byte("x"))) {
		t.Error("prefix matching is field by field")
	}
	if ns.Equal(Namespace{[]byte("live")}) || ns.Equal(Namespace{[]byte("live"), []byte("news")}) {
		t.Error("unequal namespaces compared equal")
	}
	cl := ns.Clone()
	ns[0][0] = 'L'
	if string(cl[0]) != "live" || Namespace(nil).Clone() != nil {
		t.Error("Clone should copy the field bytes")
	}

	r = NewReader(AppendNamespace(nil, Namespace{}))
	if empty := r.Namespace(); empty == nil || len(empty) != 0 || r.Done() != nil {
		t.Error("a zero-field namespace is valid")
	}
	r = NewReader(AppendNamespace(nil, Namespace{[]byte{}}))
	if r.Namespace(); !errors.Is(r.Err(), ErrEmptyNamespaceField) {
		t.Errorf("an empty field should fail, got %v", r.Err())
	}
	many := make(Namespace, MaxNamespaceFields+1)
	for i := range many {
		many[i] = []byte("f")
	}
	r = NewReader(AppendNamespace(nil, many))
	if r.Namespace(); !errors.Is(r.Err(), ErrNamespaceTooLong) {
		t.Errorf("too many fields should fail, got %v", r.Err())
	}
	big := Namespace{bytes.Repeat([]byte("a"), 3000), bytes.Repeat([]byte("b"), 1200)}
	r = NewReader(AppendNamespace(nil, big))
	if r.Namespace(); !errors.Is(r.Err(), ErrNamespaceTooLong) {
		t.Errorf("a namespace over 4096 bytes should fail, got %v", r.Err())
	}
	near := Namespace{bytes.Repeat([]byte("a"), 4000)}
	r = NewReader(AppendBytes(AppendNamespace(nil, near), bytes.Repeat([]byte("n"), 100)))
	if r.TrackName(r.Namespace()); !errors.Is(r.Err(), ErrNamespaceTooLong) {
		t.Errorf("a full track name over 4096 bytes should fail, got %v", r.Err())
	}
	r = NewReader([]byte{0x01, 0x80})
	if r.Namespace() != nil || r.Err() == nil {
		t.Error("a truncated field should fail")
	}
	for _, tc := range []struct {
		ns   Namespace
		name []byte
		want error
	}{
		{ns, []byte("video"), nil},
		{Namespace{}, nil, nil},
		{Namespace{[]byte{}}, nil, ErrEmptyNamespaceField},
		{many, nil, ErrNamespaceTooLong},
		{big, nil, ErrNamespaceTooLong},
		{near, bytes.Repeat([]byte("n"), 100), ErrNamespaceTooLong},
	} {
		if err := tc.ns.Validate(tc.name); !errors.Is(err, tc.want) {
			t.Errorf("Validate(%d fields, %d byte name) = %v, want %v", len(tc.ns), len(tc.name), err, tc.want)
		}
	}
}

func TestReasonAndURI(t *testing.T) {
	r := NewReader(AppendString(nil, "bye"))
	if r.Reason() != "bye" {
		t.Error("reason round trip")
	}
	r = NewReader(AppendString(nil, strings.Repeat("x", MaxReasonLen+1)))
	if r.Reason(); !errors.Is(r.Err(), ErrTooLong) {
		t.Error("a reason past 1024 bytes should fail")
	}
	r = NewReader(AppendString(nil, strings.Repeat("x", MaxURILen+1)))
	if r.URI(); !errors.Is(r.Err(), ErrTooLong) {
		t.Error("a URI past 8192 bytes should fail")
	}
}

func TestKVs(t *testing.T) {
	kvs := []KV{{Type: 0x0b, Bytes: []byte("imm")}, {Type: 0x04, Uint: 5000}, {Type: 0x0e, Uint: 7}, {Type: 0x0e, Uint: 9}}
	SortKVs(kvs)
	b, err := AppendKVs(nil, kvs)
	if err != nil {
		t.Fatal(err)
	}
	// delta-encoded types: 4, then 7 (to 0x0b), then 3 (to 0x0e), then 0 (a repeat)
	if b[0] != 0x04 || ValidateKVs(b) != nil {
		t.Fatalf("encoding %x", b)
	}
	var got []KV
	it := NewKVIter(b)
	for kv, ok := it.Next(); ok; kv, ok = it.Next() {
		got = append(got, kv)
	}
	if it.Err() != nil || len(got) != 4 || got[0].Uint != 5000 || string(got[1].Bytes) != "imm" ||
		got[3].Type != 0x0e || got[3].Uint != 9 {
		t.Fatalf("iterated %+v (%v)", got, it.Err())
	}
	if kv, ok := FindKV(b, 0x0e); !ok || kv.Uint != 7 {
		t.Error("FindKV should return the first pair of a type")
	}
	if _, ok := FindKV(b, 0x05); ok {
		t.Error("FindKV found an absent type")
	}
	if _, ok := FindKV(b, 0x40); ok {
		t.Error("FindKV found a type past the end")
	}

	if _, err := AppendKVs(nil, []KV{{Type: 4}, {Type: 2}}); !errors.Is(err, ErrTypeOrder) {
		t.Error("descending types should be refused")
	}
	if _, err := AppendKVs(nil, []KV{{Type: 1, Bytes: make([]byte, MaxKVValueLen+1)}}); !errors.Is(err, ErrTooLong) {
		t.Error("an oversized value should be refused")
	}
	overflow := AppendVarint(AppendVarint(AppendVarint(nil, math.MaxUint64-1), 0), 2)
	if err := ValidateKVs(overflow); !errors.Is(err, ErrTypeOverflow) {
		t.Errorf("an overflowing type delta should fail, got %v", err)
	}
	big := AppendVarint(AppendVarint(nil, 1), MaxKVValueLen+1)
	if err := ValidateKVs(big); !errors.Is(err, ErrTooLong) {
		t.Errorf("an oversized value should fail, got %v", err)
	}
	if err := ValidateKVs([]byte{0x02}); !errors.Is(err, ErrShort) {
		t.Errorf("a pair missing its value should fail, got %v", err)
	}
	if err := ValidateKVs(nil); err != nil {
		t.Error("an empty block is valid")
	}
}

func FuzzKVIter(f *testing.F) {
	f.Add([]byte{0x04, 0x88, 0x13, 0x07, 0x03, 'i', 'm', 'm'})
	f.Add([]byte{0x01, 0x80})
	f.Fuzz(func(t *testing.T, b []byte) {
		it := NewKVIter(b)
		var kvs []KV
		for kv, ok := it.Next(); ok; kv, ok = it.Next() {
			kvs = append(kvs, kv)
		}
		if it.Err() != nil {
			return
		}
		// a well-framed block re-encodes to pairs that iterate identically
		out, err := AppendKVs(nil, kvs)
		if err != nil {
			t.Fatal(err)
		}
		it = NewKVIter(out)
		for i := 0; ; i++ {
			kv, ok := it.Next()
			if !ok {
				if i != len(kvs) {
					t.Fatalf("re-encoded block holds %d pairs, want %d", i, len(kvs))
				}
				break
			}
			if kv.Type != kvs[i].Type || kv.Uint != kvs[i].Uint || !bytes.Equal(kv.Bytes, kvs[i].Bytes) {
				t.Fatalf("pair %d changed", i)
			}
		}
	})
}

func TestToken(t *testing.T) {
	tests := []Token{
		{AliasType: TokenDelete, Alias: 3},
		{AliasType: TokenUseAlias, Alias: 9},
		{AliasType: TokenRegister, Alias: 1, Type: 0, Value: []byte("secret")},
		{AliasType: TokenUseValue, Type: 2, Value: []byte("jwt")},
		{AliasType: TokenUseValue, Type: 2},
	}
	for _, tok := range tests {
		b, err := AppendToken(nil, tok)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseToken(b)
		if err != nil || got.AliasType != tok.AliasType || got.Alias != tok.Alias || got.Type != tok.Type ||
			!bytes.Equal(got.Value, tok.Value) {
			t.Errorf("%+v round-tripped to %+v (%v)", tok, got, err)
		}
	}
	if _, err := AppendToken(nil, Token{AliasType: 9}); !errors.Is(err, ErrToken) {
		t.Error("an unknown alias type should not encode")
	}
	for _, bad := range [][]byte{{0x09}, {0x00}, {0x02, 0x01, 0x05}, {}} {
		if _, err := ParseToken(bad); !errors.Is(err, ErrToken) {
			t.Errorf("%x should fail to parse", bad)
		}
	}
}

func TestRanges(t *testing.T) {
	// the draft's example: ranges 3-5 and 10-15 encode as Start=3, End=2, Start=5, End=5
	rs := []Range{{Start: 3, End: 5}, {Start: 10, End: 15}}
	b, err := AppendRanges(nil, rs)
	if err != nil || !bytes.Equal(b, []byte{3, 2, 5, 5}) {
		t.Fatalf("encoded %v (%v)", b, err)
	}
	got, err := ParseRanges(b)
	if err != nil || !slices.Equal(got, rs) {
		t.Fatalf("parsed %+v (%v)", got, err)
	}
	open := []Range{{Start: 1, End: 2}, {Start: 7, Open: true}}
	b, _ = AppendRanges(nil, open)
	if got, err := ParseRanges(b); err != nil || !slices.Equal(got, open) {
		t.Fatalf("open range parsed %+v (%v)", got, err)
	}
	if !open[1].Contains(1<<60) || open[0].Contains(3) || !open[0].Contains(1) {
		t.Error("Contains")
	}
	for _, bad := range [][]Range{
		{{Start: 5, End: 4}},
		{{Start: 5, End: 9}, {Start: 4, End: 10}},
		{{Start: 1, Open: true}, {Start: 5, End: 6}},
	} {
		if _, err := AppendRanges(nil, bad); !errors.Is(err, ErrRangeOrder) {
			t.Errorf("%+v should be refused", bad)
		}
	}
	over := AppendVarint(AppendVarint(nil, math.MaxUint64), 1)
	if _, err := ParseRanges(over); !errors.Is(err, ErrRangeOverflow) {
		t.Errorf("an overflowing end should fail, got %v", err)
	}
	over = AppendVarint(AppendVarint(AppendVarint(nil, math.MaxUint64-1), 1), 5)
	if _, err := ParseRanges(over); !errors.Is(err, ErrRangeOverflow) {
		t.Errorf("an overflowing start should fail, got %v", err)
	}
	if _, err := ParseRanges([]byte{0x80}); err == nil {
		t.Error("a truncated start should fail")
	}
	if _, err := ParseRanges([]byte{0x01, 0x80}); err == nil {
		t.Error("a truncated end should fail")
	}
}

func TestMessageFraming(t *testing.T) {
	b, at := BeginMessage(nil, 0x2f00)
	b = append(b, []byte("body")...)
	b, err := EndMessage(b, at)
	if err != nil {
		t.Fatal(err)
	}
	b2, at2 := BeginMessage(nil, 0x10)
	b2, _ = EndMessage(b2, at2)
	stream := bufio.NewReader(bytes.NewReader(append(b, b2...)))
	buf := make([]byte, 0, 2)
	typ, body, err := ReadMessage(stream, buf)
	if err != nil || typ != 0x2f00 || string(body) != "body" {
		t.Fatalf("got %#x %q %v", typ, body, err)
	}
	typ, body, err = ReadMessage(stream, buf)
	if err != nil || typ != 0x10 || len(body) != 0 {
		t.Fatalf("got %#x %q %v", typ, body, err)
	}
	if _, _, err = ReadMessage(stream, buf); !errors.Is(err, io.EOF) {
		t.Errorf("a clean end should be io.EOF, got %v", err)
	}

	tooLong, at := BeginMessage(nil, 1)
	if _, err := EndMessage(append(tooLong, make([]byte, MaxMessageLen+1)...), at); !errors.Is(err, ErrMessageTooLong) {
		t.Error("a body past 65535 bytes should be refused")
	}
	for _, partial := range [][]byte{{0x80}, {0x05, 0x00}, {0x05, 0x00, 0x03, 'a'}} {
		_, _, err := ReadMessage(bufio.NewReader(bytes.NewReader(partial)), nil)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%x: a cut message should be io.ErrUnexpectedEOF, got %v", partial, err)
		}
	}
	if v, err := ReadVarintFrom(bufio.NewReader(bytes.NewReader(AppendVarint(nil, 1<<50)))); err != nil || v != 1<<50 {
		t.Errorf("ReadVarintFrom got %d %v", v, err)
	}
}
