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
	"crypto/tls"
	"sync"
	"testing"

	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func testEntry(t *testing.T, key string, names ...string) *Entry {
	t.Helper()
	k, c, err := tlstest.GetTestKeyAndCertWithNames(names...)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ValidatePair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	return NewEntry(key, SourceKindFile, cert)
}

func helloFor(host string) *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{ServerName: host}
}

func TestGetCertSNISelection(t *testing.T) {
	e1 := testEntry(t, "e1", "www.example.com", "example.com")
	e2 := testEntry(t, "e2", "*.wild.example.com")
	e3 := testEntry(t, "e3", "trickster.io")
	store := NewStore([]*Entry{e1, e2, e3})

	tests := []struct {
		name     string
		sni      string
		expected *Entry
	}{
		{"exact match", "www.example.com", e1},
		{"exact match secondary SAN", "example.com", e1},
		{"exact match other cert", "trickster.io", e3},
		{"wildcard match", "foo.wild.example.com", e2},
		{"case-insensitive with trailing dot", "WWW.Example.COM.", e1},
		{"unknown host falls back to first", "unknown.invalid", e1},
		{"no SNI falls back to first", "", e1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cert, err := store.GetCert(helloFor(test.sni))
			if err != nil {
				t.Fatal(err)
			}
			if cert.Leaf == nil || test.expected.Certificate.Leaf == nil ||
				!cert.Leaf.Equal(test.expected.Certificate.Leaf) {
				t.Errorf("wrong certificate selected for %q", test.sni)
			}
		})
	}
}

func TestGetCertSingleAndEmpty(t *testing.T) {
	store := NewStore(nil)
	if _, err := store.GetCert(helloFor("any")); err != ErrNoCertificates {
		t.Errorf("expected ErrNoCertificates, got %v", err)
	}
	e1 := testEntry(t, "e1", "one.example.com")
	store.SetEntries([]*Entry{e1})
	// a single cert is always returned, even on SNI mismatch, preserving the
	// legacy swapper behavior
	cert, err := store.GetCert(helloFor("mismatch.invalid"))
	if err != nil {
		t.Fatal(err)
	}
	if !cert.Leaf.Equal(e1.Certificate.Leaf) {
		t.Error("expected the sole certificate to be returned")
	}
}

func TestSetEntriesDedupe(t *testing.T) {
	e1 := testEntry(t, "e1", "dupe.example.com")
	e2 := &Entry{
		Key: "e2", SourceKind: SourceKindMemory,
		Certificate: e1.Certificate, ContentHash: e1.ContentHash,
	}
	e3 := testEntry(t, "e3", "other.example.com")
	store := NewStore([]*Entry{e1, e2, e3})
	entries := store.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after dedupe, got %d", len(entries))
	}
	if entries[0].Key != "e1" || entries[1].Key != "e3" {
		t.Errorf("unexpected entry keys after dedupe: %s, %s",
			entries[0].Key, entries[1].Key)
	}
}

func TestEntriesMetadata(t *testing.T) {
	e1 := testEntry(t, "e1", "meta.example.com")
	store := NewStore([]*Entry{e1})
	entries := store.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	info := entries[0]
	if info.SourceKind != SourceKindFile || info.CommonName != "meta.example.com" ||
		len(info.DNSNames) != 1 || info.DNSNames[0] != "meta.example.com" ||
		info.NotAfter.IsZero() || info.LastLoad.IsZero() {
		t.Errorf("unexpected entry metadata: %+v", info)
	}
}

func TestConcurrentSwapAndHandshake(t *testing.T) {
	e1 := testEntry(t, "e1", "a.example.com")
	e2 := testEntry(t, "e2", "b.example.com")
	store := NewStore([]*Entry{e1, e2})
	var wg sync.WaitGroup
	done := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := store.GetCert(helloFor("a.example.com")); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	for range 200 {
		store.SetEntries([]*Entry{e2, e1})
		store.SetEntries([]*Entry{e1, e2})
	}
	close(done)
	wg.Wait()
}

func TestValidatePair(t *testing.T) {
	k1, c1, err := tlstest.GetTestKeyAndCertWithNames("pair.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePair(c1, k1); err != nil {
		t.Error(err)
	}
	// a mismatched pair (mid-rotation state) must fail validation
	k2, _, err := tlstest.GetTestKeyAndCertWithNames("pair.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePair(c1, k2); err == nil {
		t.Error("expected validation error for mismatched pair")
	}
	if _, err := ValidatePair([]byte("garbage"), k1); err == nil {
		t.Error("expected validation error for invalid cert PEM")
	}
}
