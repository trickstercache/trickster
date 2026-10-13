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

package global

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// quoteTestLimits is how many limits a string is cut at before only a sample of them is
const quoteTestLimits = 1024

// quoteTestStrings are strings with every kind of character JSON escapes, and invalid UTF-8 of every shape
var quoteTestStrings = []string{
	"", "plain", "a\"<b>\\\n\xff \u00fc ", "\u2028", "\u2029", "a\u2028b\u2029c", "\u2027\u202a", "\xed\xa0\x80",
	"\xf4\x90\x80\x80", "\xc0\x80", "\xe2\x80", "\xe2", "\u00fc\xff\u20ac\x80\U0001f600", "\ufffd", "\x7f",
	"\b\f\n\r\t\x00\x1f", "<script>&amp;</script>", strings.Repeat("&<>", 50), "trailing\xf0\x9f\x98",
}

// validTestUTF8 replaces each byte of invalid UTF-8 in s with U+FFFD
func validTestUTF8(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if size == 1 && r == utf8.RuneError {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// marshalTestString returns s as encoding/json quotes it, writing U+FFFD for each byte of invalid UTF-8 as its v2 encoder
// does, where its v1 encoder writes the same character escaped
func marshalTestString(t *testing.T, s string) []byte {
	t.Helper()
	b, err := json.Marshal(validTestUTF8(s))
	require.NoError(t, err)
	return b
}

// requireQuotedAsJSON requires s to quote, measure and cut exactly as encoding/json quotes it
func requireQuotedAsJSON(t *testing.T, s string) {
	t.Helper()
	want := marshalTestString(t, s)
	n, first := quotedLen(s, math.MaxInt)
	require.Equal(t, len(want), n, "%q", s)
	require.Equal(t, string(want), string(appendQuoted(nil, s, first)), "%q", s)
	require.Equal(t, string(want), string(appendQuoted(nil, s, 0)), "%q", s)
	require.Equal(t, s[:first], string(want[1:first+1]), "%q: nothing before %d is escaped", s, first)
	exact, _ := quotedLen(s, n)
	require.Equal(t, n, exact, "%q: measured within its own length", s)
	// every limit of a short string, and a sample of a long one's
	for limit := 0; limit < n; limit += 1 + n/quoteTestLimits {
		over, _ := quotedLen(s, limit)
		require.Greater(t, over, limit, "%q: a length past %d", s, limit)
		cut, size := quotedPrefix(s, limit)
		if limit < len(`""`) {
			// no limit is below the empty string's
			require.Zero(t, cut)
			require.Equal(t, len(`""`), size)
			continue
		}
		prefix := marshalTestString(t, s[:cut])
		require.Equal(t, len(prefix), size, "%q: the length of its first %d bytes", s, cut)
		require.LessOrEqual(t, size, limit, "%q: a prefix within %d", s, limit)
		require.True(t, strings.HasPrefix(string(want), string(prefix[:len(prefix)-1])),
			"%q: a prefix of %d bytes quotes as the start of the whole", s, cut)
		require.Less(t, cut, len(s), "%q: no more than its quoted length fits all of it", s)
		_, next := quotedChar(s, cut)
		require.Greater(t, size+next, limit, "%q: a prefix within %d is the longest", s, limit)
	}
	cut, size := quotedPrefix(s, n)
	require.Equal(t, len(s), cut)
	require.Equal(t, n, size)
}

func TestQuotedMatchesEncodingJSON(t *testing.T) {
	for _, s := range quoteTestStrings {
		requireQuotedAsJSON(t, s)
	}
	for c := range 256 {
		requireQuotedAsJSON(t, string([]byte{byte(c)}))
		for d := range 256 {
			s := string([]byte{byte(c), byte(d)})
			want := marshalTestString(t, s)
			n, first := quotedLen(s, math.MaxInt)
			require.Equal(t, len(want), n, "%q", s)
			require.Equal(t, string(want), string(appendQuoted(nil, s, first)), "%q", s)
		}
	}
}

func FuzzQuoted(f *testing.F) {
	for _, s := range quoteTestStrings {
		f.Add(s)
	}
	f.Fuzz(requireQuotedAsJSON)
}
