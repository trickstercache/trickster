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

package sqlscan

import (
	"slices"
	"strings"
	"testing"
)

type scanned struct {
	kind Kind
	text string
}

func scanAll(src string, opts Options) ([]scanned, *Scanner) {
	s := New(src, opts)
	var out []scanned
	for {
		token, ok := s.Next()
		if !ok {
			return out, s
		}
		out = append(out, scanned{token.Kind, s.Text(token)})
	}
}

func TestScannerTokens(t *testing.T) {
	for name, test := range map[string]struct {
		src  string
		opts Options
		want []scanned
	}{
		"words numbers and punctuation": {
			src:  "SELECT a1, 1.5e+3, .5 FROM t",
			want: []scanned{{Word, "SELECT"}, {Word, "a1"}, {Punct, ","}, {Number, "1.5e+3"}, {Punct, ","}, {Number, ".5"}, {Word, "FROM"}, {Word, "t"}},
		},
		"doubled quotes stay inside": {
			src:  `'it''s; here' "a""b"`,
			want: []scanned{{String, `'it''s; here'`}, {QuotedIdent, `"a""b"`}},
		},
		"escape string always honors backslashes": {
			src:  `E'a\'; b' x`,
			want: []scanned{{String, `E'a\'; b'`}, {Word, "x"}},
		},
		"plain string ignores backslashes by default": {
			src:  `'a\' x`,
			want: []scanned{{String, `'a\'`}, {Word, "x"}},
		},
		"plain string honors backslashes when asked": {
			src:  `'a\' x'`,
			opts: Options{BackslashEscapes: true},
			want: []scanned{{String, `'a\' x'`}},
		},
		"prefixed constants": {
			src:  `U&'d\0061t' U&"d" B'01' x'ff' n'q' u & e`,
			want: []scanned{{String, `U&'d\0061t'`}, {QuotedIdent, `U&"d"`}, {String, "B'01'"}, {String, "x'ff'"}, {String, "n'q'"}, {Word, "u"}, {Punct, "&"}, {Word, "e"}},
		},
		"dollar quotes and parameters": {
			src:  "$$a;b$$ $fn$ x $$ y $fn$ $1 $ 2",
			want: []scanned{{String, "$$a;b$$"}, {String, "$fn$ x $$ y $fn$"}, {Param, "$1"}, {Punct, "$"}, {Number, "2"}},
		},
		"comments are skipped and block comments nest": {
			src:  "a -- x; y\n/* b /* c */ ; */ d --",
			want: []scanned{{Word, "a"}, {Word, "d"}},
		},
		"identifier bytes above ascii": {
			src:  "sélect tab$le",
			want: []scanned{{Word, "sélect"}, {Word, "tab$le"}},
		},
	} {
		got, s := scanAll(test.src, test.opts)
		if !slices.Equal(got, test.want) || s.Unterminated {
			t.Fatalf("%s: got %v (unterminated %t), want %v", name, got, s.Unterminated, test.want)
		}
	}
}

func TestScannerUnterminated(t *testing.T) {
	for _, src := range []string{"'abc", `"abc`, "/* abc", "$$abc", "$tag$abc$ta$", `E'abc\`} {
		got, s := scanAll("x "+src, Options{})
		if !s.Unterminated || len(got) == 0 || got[0].text != "x" {
			t.Fatalf("%q: expected an unterminated scan, got %v", src, got)
		}
	}
}

func TestScannerDepth(t *testing.T) {
	s := New("f(a, (b)) ) c", Options{})
	var depths []int
	for {
		token, ok := s.Next()
		if !ok {
			break
		}
		depths = append(depths, token.Depth)
	}
	// f ( a , ( b ) ) ) c : the stray closing parenthesis must not go negative
	want := []int{0, 0, 1, 1, 1, 2, 1, 0, 0, 0}
	if !slices.Equal(depths, want) {
		t.Fatalf("depths %v, want %v", depths, want)
	}
}

func TestIsWord(t *testing.T) {
	s := New(`select "select"`, Options{})
	first, _ := s.Next()
	second, _ := s.Next()
	if !s.IsWord(first, "SELECT") || s.IsWord(second, "select") || s.IsWord(first, "selec") {
		t.Fatal("only the unquoted word may match, without regard to case")
	}
}

func FuzzScanner(f *testing.F) {
	f.Add("SELECT 'a' FROM t -- c", false)
	f.Add("$a$ /* x */ E'\\'' U&\"q\" $1 1e+5", true)
	f.Fuzz(func(t *testing.T, src string, backslash bool) {
		s := New(src, Options{BackslashEscapes: backslash})
		end := 0
		for {
			token, ok := s.Next()
			if !ok {
				break
			}
			if token.Start < end || token.End <= token.Start || token.End > len(src) || token.Depth < 0 {
				t.Fatalf("invalid token %+v after offset %d in %q", token, end, src)
			}
			// only ASCII whitespace separates tokens; PostgreSQL reads other
			// space-like runes as identifier characters
			if strings.Trim(src[token.Start:token.End], " \t\n\r\f\v") == "" {
				t.Fatalf("whitespace token %+v in %q", token, src)
			}
			end = token.End
		}
	})
}
