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

package strings

import (
	"encoding/json"
	"math"
	"testing"
	"unicode/utf8"
)

func requireMarshalMatch(t *testing.T, s string) {
	t.Helper()
	got := AppendJSON(nil, s)
	want, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.ValidString(s) {
		if string(got) != string(want) {
			t.Fatalf("%q: got %s want %s", s, got, want)
		}
		return
	}
	// invalid UTF-8 is written as a replacement character, which encoding/json may escape
	var a, b string
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("%q: %s is not a JSON string: %v", s, got, err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("%q: got %q want %q", s, a, b)
	}
}

func TestAppendJSON(t *testing.T) {
	cases := []string{
		"", "trickster", `q"uo\te`, "\b\f\n\r\t", "\x00\x01\x1f\x7f", "<a href='x'>&amp;</a>",
		"é ü 日本 😀", "  ", "\xff", "a\xfeb\xc3", "\xed\xa0\x80", "‧‪",
	}
	for _, s := range cases {
		requireMarshalMatch(t, s)
	}
	var all [256]byte
	for i := range all {
		all[i] = byte(i)
	}
	requireMarshalMatch(t, string(all[:]))
	if got := string(AppendJSON([]byte("x:"), "y")); got != `x:"y"` {
		t.Errorf("got %s", got)
	}
}

func FuzzAppendJSON(f *testing.F) {
	for _, s := range []string{"", "trickster", "<>&\"\\", " \xff\x00"} {
		f.Add(s)
	}
	f.Fuzz(requireMarshalMatch)
}

func requireFloatMatch(t *testing.T, f float64) {
	t.Helper()
	want, wantErr := json.Marshal(f)
	got, ok := AppendJSONFloat(nil, f, 64)
	if ok != (wantErr == nil) || (ok && string(got) != string(want)) {
		t.Fatalf("%v: got %s, %v; want %s, %v", f, got, ok, want, wantErr)
	}
	f32 := float32(f)
	want, wantErr = json.Marshal(f32)
	got, ok = AppendJSONFloat(nil, float64(f32), 32)
	if ok != (wantErr == nil) || (ok && string(got) != string(want)) {
		t.Fatalf("float32 %v: got %s, %v; want %s, %v", f32, got, ok, want, wantErr)
	}
}

func TestAppendJSONFloat(t *testing.T) {
	for _, f := range []float64{
		0, math.Copysign(0, -1), 1, -1, 0.1, 1.5, 100, 1e20, 1e21, 1e22, -1e21, 1e-6, 1e-7, 9.99e-7,
		1.2e-9, 1.2e-10, 123456789.125, math.MaxFloat64, math.SmallestNonzeroFloat64, math.MaxFloat32,
		math.NaN(), math.Inf(1), math.Inf(-1), 3.4028234663852886e38 * 2,
	} {
		requireFloatMatch(t, f)
	}
}

func FuzzAppendJSONFloat(f *testing.F) {
	for _, v := range []float64{0, 1e21, 1e-7, 1.5, -2.5e-9} {
		f.Add(v)
	}
	f.Fuzz(requireFloatMatch)
}

type jsonValueCase struct {
	A int `json:"a"`
}

func TestAppendJSONValue(t *testing.T) {
	for _, v := range []any{
		nil, "x<y>&\"", true, false, 1.5, float32(0.1), 1e-7, int64(-9), 12, int32(-3), uint64(18446744073709551615),
		uint(7), uint32(9), json.Number("12.50"),
		[]any{1.0, "a", nil},
		map[string]any{"b": 1, "a": "<"},
		jsonValueCase{A: 3},
		int8(4), []byte("raw"),
	} {
		want, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckJSONValue(v); err != nil {
			t.Fatalf("%#v: %v", v, err)
		}
		got, err := AppendJSONValue([]byte("x"), v)
		if err != nil || string(got) != "x"+string(want) {
			t.Fatalf("%#v: got %s, %v; want %s", v, got, err, want)
		}
	}
	for _, v := range []any{math.NaN(), float32(math.Inf(1)), func() {}} {
		_, want := json.Marshal(v)
		if err := CheckJSONValue(v); err == nil || err.Error() != want.Error() {
			t.Fatalf("%#v: checked %v; want %v", v, err, want)
		}
		got, err := AppendJSONValue([]byte("x"), v)
		if err == nil || err.Error() != want.Error() || string(got) != "x" {
			t.Fatalf("%#v: got %s, %v; want %v", v, got, err, want)
		}
	}
}
