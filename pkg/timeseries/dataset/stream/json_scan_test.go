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

package stream

import (
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

// the scanners as they read a byte at a time, the oracle for the ones that read eight
func byteStringEnd(raw []byte, i int) int {
	for j := i + 1; j < len(raw); j++ {
		switch raw[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(raw)
}

func bytePlainASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 || c == '\\' {
			return false
		}
	}
	return true
}

func TestScansMatchBytewise(t *testing.T) {
	rng := weaktest.NewRand(51, 51)
	pieces := []string{"a", "bc", "0123456", `\"`, `\\`, `\n`, `é`, "é", "\xff", " ", "\x7f", "\x80"}
	for range 20000 {
		var b strings.Builder
		b.WriteByte('"')
		for range rng.IntN(12) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		b.WriteByte('"')
		// what follows a string, which its scan must stop before
		b.WriteString([]string{"", ",", `,"x":1`, `"`, `\`}[rng.IntN(5)])
		raw := []byte(b.String())
		require.Equal(t, byteStringEnd(raw, 0), stringEnd(raw, 0), "%q", raw)
		for i := range len(raw) + 1 {
			require.Equal(t, bytePlainASCII(raw[i:]), plainASCII(raw[i:]), "%q from %d", raw, i)
		}
	}
	// a string cut short ends with its input
	require.Equal(t, 9, stringEnd([]byte(`"abcdefgh`), 0))
	require.Equal(t, 10, stringEnd([]byte(`"abcdefg\"`), 0))
}

func BenchmarkScans(b *testing.B) {
	for _, s := range []string{`"host-12"`, `"2024-01-01T00:00:00.000Z"`, `"a longer label value, as a tag can hold"`} {
		raw := []byte(s)
		b.Run("stringEnd/"+s, func(b *testing.B) {
			for b.Loop() {
				stringEnd(raw, 0)
			}
		})
		b.Run("bytewise/"+s, func(b *testing.B) {
			for b.Loop() {
				byteStringEnd(raw, 0)
			}
		})
		b.Run("plainASCII/"+s, func(b *testing.B) {
			for b.Loop() {
				plainASCII(raw)
			}
		})
		b.Run("bytewisePlain/"+s, func(b *testing.B) {
			for b.Loop() {
				bytePlainASCII(raw)
			}
		})
	}
}
