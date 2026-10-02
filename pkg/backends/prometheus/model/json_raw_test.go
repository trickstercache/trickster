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

package model

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

// appendMarshaled re-encodes raw with a new marshaler
func appendMarshaled(dst, raw []byte) ([]byte, error) {
	var m marshaler
	return m.append(dst, raw)
}

// marshaledByEncodingJSON is what appendMarshaled must match: raw decoded into an any and marshaled
func marshaledByEncodingJSON(raw []byte) (string, bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	b, err := json.Marshal(v)
	return string(b), err == nil
}

var jsonNumbers = []string{
	"0", "-0", "7", "-12", "123456789012345", "1234567890123456", "9999999999999999", "-9999999999999999", "12345678901234567890", "1.0", "2.5",
	"-0.0", "1e3", "1E-7", "1e-6", "0.000001", "2.938735877055719e-39", "1e21", "9.99e20", "1e+21",
	"123456789012345678", "0.1", "3.14159265358979323846", "5e-324", "1.7976931348623157e308",
}

var jsonStrings = []string{
	`""`, `"a"`, `"3.14"`, `"a\"b"`, `"\\"`, `"\/"`, `"A"`, `"<a&b>"`, `" "`, `"é"`,
	`"😀"`, `"\n\t\b\f\r"`, `"\u0001"`, "\"\x7f\"", `"éé"`,
}

func randJSON(rng *weaktest.Rand, depth int) string {
	switch k := rng.IntN(8); {
	case depth > 3 || k < 3:
		switch rng.IntN(4) {
		case 0:
			return jsonNumbers[rng.IntN(len(jsonNumbers))]
		case 1:
			return jsonStrings[rng.IntN(len(jsonStrings))]
		case 2:
			return []string{"true", "false", "null"}[rng.IntN(3)]
		}
		return strconv.Itoa(rng.IntN(100) - 50)
	case k < 5:
		n := rng.IntN(4)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = randJSON(rng, depth+1)
		}
		return "[" + strings.Join(parts, " , ") + "]"
	}
	names := []string{`"a"`, `"b"`, `"B"`, `"count"`, `"sum"`, `"é"`, `"a"`, `"<"`, `"z"`, `""`}
	n := rng.IntN(5)
	parts := make([]string, n)
	for i := range parts {
		// names repeat, as encoding/json keeps the last of a repeated name
		parts[i] = names[rng.IntN(len(names))] + ":" + randJSON(rng, depth+1)
	}
	return "{ " + strings.Join(parts, ",\n") + " }"
}

func TestAppendMarshaledMatchesEncodingJSON(t *testing.T) {
	rng := weaktest.NewRand(31, 32)
	// a marshaler reused across values, as a decoder reuses one, must write each as a new one does
	var shared marshaler
	for range 20000 {
		raw := randJSON(rng, 0)
		want, ok := marshaledByEncodingJSON([]byte(raw))
		got, err := appendMarshaled([]byte("prefix"), []byte(raw))
		reused, reusedErr := shared.append(nil, []byte(raw))
		if !ok {
			require.Error(t, err, raw)
			require.Error(t, reusedErr, raw)
			continue
		}
		require.NoError(t, err, raw)
		require.Equal(t, "prefix"+want, string(got), raw)
		require.NoError(t, reusedErr, raw)
		require.Equal(t, want, string(reused), raw)
	}
	for _, raw := range []string{"1e400", `{"a":-1e400}`, `[1e999]`} {
		_, err := appendMarshaled(nil, []byte(raw))
		require.Error(t, err, raw)
	}
	_, err := appendMarshaled(nil, []byte("  "))
	require.Error(t, err)
}

func FuzzAppendMarshaled(f *testing.F) {
	for _, s := range append(append([]string{`{"count":"10","sum":"3.14"}`}, jsonNumbers...), jsonStrings...) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if !json.Valid([]byte(raw)) {
			return
		}
		want, ok := marshaledByEncodingJSON([]byte(raw))
		got, err := appendMarshaled(nil, []byte(raw))
		if !ok {
			if err == nil {
				t.Fatalf("%q: encoding/json failed, appendMarshaled did not", raw)
			}
			return
		}
		if err != nil || string(got) != want {
			t.Fatalf("%q: got %q (%v), want %q", raw, got, err, want)
		}
	})
}
