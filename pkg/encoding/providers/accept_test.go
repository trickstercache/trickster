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

package providers

import (
	"slices"
	"testing"
)

func acceptedList(a Accepted) []Provider {
	out := make([]Provider, 0, a.Len())
	for i := range a.Len() {
		out = append(out, a.At(i))
	}
	return out
}

func TestParseAcceptEncoding(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		expected []Provider
		header   string
	}{
		{"none", nil, nil, ""},
		{"empty", []string{""}, nil, ""},
		{"unsupported", []string{"compress, identity, x-unknown"}, nil, ""},
		// without weights the client has no preference, so Trickster's own applies
		{"unweighted", []string{"gzip, deflate, br, zstd"},
			[]Provider{Zstandard, Brotli, GZip, Deflate}, "zstd, br, gzip, deflate"},
		{"unweighted subset", []string{"deflate, gzip"}, []Provider{GZip, Deflate}, "gzip, deflate"},
		{"weighted", []string{"gzip;q=1.0, zstd;q=0.5, br;q=0.8"},
			[]Provider{GZip, Brotli, Zstandard}, "gzip, br;q=0.8, zstd;q=0.5"},
		{"weights and defaults", []string{"br;q=0.9, gzip, zstd;q=0.9"},
			[]Provider{GZip, Zstandard, Brotli}, "gzip, zstd;q=0.9, br;q=0.9"},
		{"all weighted alike", []string{"gzip;q=0.5, zstd;q=0.5"},
			[]Provider{Zstandard, GZip}, "zstd;q=0.5, gzip;q=0.5"},
		{"refused", []string{"gzip;q=0, br;q=0.0, zstd;q=0.000, deflate"}, []Provider{Deflate}, "deflate"},
		// the wildcard stands for every coding that isn't named, at its own weight
		{"wildcard only", []string{"*"}, []Provider{Zstandard, Brotli, GZip, Deflate}, "zstd, br, gzip, deflate"},
		{"weighted wildcard", []string{"*;q=0.5"}, []Provider{Zstandard, Brotli, GZip, Deflate},
			"zstd;q=0.5, br;q=0.5, gzip;q=0.5, deflate;q=0.5"},
		{"refused with a wildcard", []string{"gzip;q=0, *;q=1"},
			[]Provider{Zstandard, Brotli, Deflate}, "zstd, br, deflate"},
		{"named over the wildcard", []string{"*;q=0.5, gzip"},
			[]Provider{GZip, Zstandard, Brotli, Deflate}, "gzip, zstd;q=0.5, br;q=0.5, deflate;q=0.5"},
		{"named under the wildcard", []string{"gzip;q=0.2, *;q=0.8"},
			[]Provider{Zstandard, Brotli, Deflate}, "zstd;q=0.8, br;q=0.8, deflate;q=0.8"},
		{"refused wildcard", []string{"*;q=0, BR"}, []Provider{Brotli}, "br"},
		{"repeated wildcard", []string{"*;q=0, *, gzip"}, []Provider{GZip}, "gzip"},
		// identity is always there to be sent, so a coding weighted below it never is
		{"identity preferred", []string{"identity;q=1, gzip;q=0.5"}, nil, ""},
		{"identity preferred to some", []string{"identity;q=0.3, gzip;q=1.0, deflate;q=0.2"}, []Provider{GZip}, "gzip"},
		{"identity weighted alike", []string{"identity;q=0.5, gzip;q=0.5"}, []Provider{GZip}, "gzip;q=0.5"},
		{"identity by the wildcard", []string{"gzip;q=0.4, *;q=0.6, br;q=0.6"},
			[]Provider{Zstandard, Brotli, Deflate}, "zstd;q=0.6, br;q=0.6, deflate;q=0.6"},
		{"identity refused", []string{"identity;q=0, gzip;q=0.1"}, []Provider{GZip}, "gzip;q=0.1"},
		{"identity refused by the wildcard", []string{"*;q=0, deflate;q=0.1"}, []Provider{Deflate}, "deflate;q=0.1"},
		{"identity named over the wildcard", []string{"*;q=0, identity, gzip;q=0.5"}, nil, ""},
		// with nothing left that is acceptable, identity is what is sent, as other servers do
		{"everything refused", []string{"identity;q=0, gzip;q=0, *;q=0"}, nil, ""},
		{"precision", []string{"gzip;q=0.001, br;q=0.125"}, []Provider{Brotli, GZip}, "br;q=0.125, gzip;q=0.001"},
		{"rounds to refused", []string{"gzip;q=0.0001, br"}, []Provider{Brotli}, "br"},
		{"case and spacing", []string{" GZIP ; Q = 0.5 ,BR"}, []Provider{Brotli, GZip}, "br, gzip;q=0.5"},
		{"other parameters", []string{"gzip;level=9;q=0.5;x=y, br;level=1"},
			[]Provider{Brotli, GZip}, "br, gzip;q=0.5"},
		// a coding that was named is not refused for a weight that can't be read
		{"malformed weights", []string{"gzip;q=high, br;q=-1, zstd;q=NaN, deflate;q"},
			[]Provider{Zstandard, Brotli, GZip, Deflate}, "zstd, br, gzip, deflate"},
		{"over weight", []string{"gzip;q=7, br;q=0.5"}, []Provider{GZip, Brotli}, "gzip, br;q=0.5"},
		{"repeated", []string{"gzip;q=0.5, gzip;q=1, br;q=0.8"}, []Provider{Brotli, GZip}, "br;q=0.8, gzip;q=0.5"},
		{"repeated after refusal", []string{"gzip;q=0, gzip"}, nil, ""},
		{"alias", []string{"x-gzip"}, []Provider{GZip}, "gzip"},
		{"lines", []string{"deflate;q=0.2", "zstd, br;q=0.4"},
			[]Provider{Zstandard, Brotli, Deflate}, "zstd, br;q=0.4, deflate;q=0.2"},
		{"empty members", []string{",, gzip,,"}, []Provider{GZip}, "gzip"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := ParseAcceptEncoding(test.values...)
			if got := acceptedList(a); !slices.Equal(got, test.expected) {
				t.Errorf("expected %v got %v", test.expected, got)
			}
			if a.String() != test.header {
				t.Errorf("expected header %q got %q", test.header, a.String())
			}
			var bitmap Provider
			for _, enc := range test.expected {
				bitmap |= enc
			}
			if a.Bitmap() != bitmap {
				t.Errorf("expected bitmap %d got %d", bitmap, a.Bitmap())
			}
			preferred := Identity
			if len(test.expected) > 0 {
				preferred = test.expected[0]
			}
			if a.Preferred() != preferred {
				t.Errorf("expected %s to be preferred, got %s", preferred, a.Preferred())
			}
			// what the header value asks for is what was accepted
			if again := ParseAcceptEncoding(a.String()); !slices.Equal(acceptedList(again), test.expected) {
				t.Errorf("expected %q to parse back to %v, got %v", a.String(), test.expected, acceptedList(again))
			}
		})
	}
}

func TestAcceptedFilter(t *testing.T) {
	a := ParseAcceptEncoding("gzip, zstd;q=0.5, br;q=0.8")
	f := a.Filter(Zstandard | GZip)
	if got := acceptedList(f); !slices.Equal(got, []Provider{GZip, Zstandard}) || f.String() != "gzip, zstd;q=0.5" {
		t.Errorf("expected the order and weights to be kept, got %v %q", got, f.String())
	}
	if a.Filter(Identity).Len() != 0 || a.Filter(Deflate).Preferred() != Identity {
		t.Error("expected nothing to pass an empty bitmap")
	}
}

func TestGetCompatibleWebProvidersWeighted(t *testing.T) {
	s, p := GetCompatibleWebProviders("gzip;q=0.8, zstd;q=0, BR")
	if s != "br, gzip;q=0.8" || p != Brotli|GZip {
		t.Errorf("expected weighted and refused codings to be honored, got %q %d", s, p)
	}
}

func BenchmarkParseAcceptEncoding(b *testing.B) {
	for name, value := range map[string]string{
		"browser":  "gzip, deflate, br, zstd",
		"weighted": "gzip;q=1.0, br;q=0.8, zstd;q=0.5",
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				a := ParseAcceptEncoding(value)
				_ = a.Bitmap()
				_ = a.Preferred()
			}
		})
	}
}
