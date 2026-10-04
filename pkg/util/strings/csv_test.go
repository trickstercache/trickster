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
	"bytes"
	"encoding/csv"
	"testing"
)

func requireCSVMatch(t *testing.T, a, b string, comma byte) {
	t.Helper()
	var want bytes.Buffer
	w := csv.NewWriter(&want)
	w.Comma = rune(comma)
	if err := w.Write([]string{a, b}); err != nil {
		t.Skip(err)
	}
	w.Flush()
	got := AppendCSVField(nil, a, comma)
	got = append(got, comma)
	got = append(AppendCSVField(got, b, comma), '\n')
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("%q, %q with %q: got %q want %q", a, b, comma, got, want.Bytes())
	}
}

func TestAppendCSVField(t *testing.T) {
	for _, s := range []string{
		"", "plain", "a,b", `q"uote`, "line\nbreak", "cr\rhere", `\.`, " lead", "\tlead", " nbsp",
		" sep", `""`, "é", "\xff", "trail ", "a\tb",
	} {
		for _, comma := range []byte{',', '\t', ';'} {
			requireCSVMatch(t, s, "x", comma)
		}
	}
}

func FuzzAppendCSVField(f *testing.F) {
	for _, s := range []string{"", "a,b", `"`, "\r\n", `\.`, " x"} {
		f.Add(s, s, byte(','))
	}
	f.Fuzz(func(t *testing.T, a, b string, comma byte) {
		if comma == '"' || comma == '\r' || comma == '\n' || comma >= 0x80 || comma == 0 {
			return
		}
		requireCSVMatch(t, a, b, comma)
	})
}
