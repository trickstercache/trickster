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

package tls

import (
	"fmt"
	"testing"

	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

// benchStore builds a store with n certificates: n-1 exact-host certs and
// one wildcard cert
func benchStore(b *testing.B, n int) CertStore {
	b.Helper()
	entries := make([]*Entry, 0, n)
	for i := range n - 1 {
		host := fmt.Sprintf("host%d.example.com", i)
		k, c, err := tlstest.GetTestKeyAndCertWithNames(host)
		if err != nil {
			b.Fatal(err)
		}
		cert, err := ValidatePair(c, k)
		if err != nil {
			b.Fatal(err)
		}
		entries = append(entries, NewEntry(host, SourceKindFile, cert))
	}
	k, c, err := tlstest.GetTestKeyAndCertWithNames("*.wild.example.com")
	if err != nil {
		b.Fatal(err)
	}
	cert, err := ValidatePair(c, k)
	if err != nil {
		b.Fatal(err)
	}
	entries = append(entries, NewEntry("wild", SourceKindFile, cert))
	return NewStore(entries)
}

// BenchmarkGetCert demonstrates that the SNI index makes per-handshake
// selection cost independent of certificate count (compare hit cases across
// sizes), while a full miss falls back to the linear scan
func BenchmarkGetCert(b *testing.B) {
	for _, n := range []int{4, 128, 512} {
		store := benchStore(b, n)
		b.Run(fmt.Sprintf("exact-hit-%d", n), func(b *testing.B) {
			chi := helloFor(fmt.Sprintf("host%d.example.com", n-2))
			b.ResetTimer()
			for range b.N {
				if _, err := store.GetCert(chi); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("wildcard-hit-%d", n), func(b *testing.B) {
			chi := helloFor("anything.wild.example.com")
			b.ResetTimer()
			for range b.N {
				if _, err := store.GetCert(chi); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("fallback-miss-%d", n), func(b *testing.B) {
			chi := helloFor("miss.invalid")
			b.ResetTimer()
			for range b.N {
				if _, err := store.GetCert(chi); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
