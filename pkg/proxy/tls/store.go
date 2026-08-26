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
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"time"
)

// source kinds for Entry.SourceKind
const (
	// SourceKindConfig identifies certificates loaded via the config reload
	// path, where no per-source identity is available.
	SourceKindConfig = "config"
	// SourceKindFile identifies certificates loaded from a watched file pair.
	SourceKindFile = "file"
	// SourceKindMemory identifies certificates supplied at runtime as
	// in-memory PEM (e.g. from a Kubernetes Secret).
	SourceKindMemory = "memory"
)

// Entry is a single certificate held by a CertStore, keyed by a stable
// source identity so it can be updated in place when its source changes.
type Entry struct {
	// Key is the stable source identity of the certificate
	Key string
	// SourceKind is one of SourceKindConfig, SourceKindFile or SourceKindMemory
	SourceKind string
	// Certificate is the parsed certificate; Leaf is always populated
	Certificate tls.Certificate
	// ContentHash is the sha256 hash of the certificate chain's DER bytes
	ContentHash string
	// LastLoad is the time the certificate was last loaded from its source
	LastLoad time.Time
}

// EntryInfo describes an Entry for read-only inventory purposes and never
// includes key material.
type EntryInfo struct {
	Key        string    `json:"id"`
	SourceKind string    `json:"source"`
	CommonName string    `json:"common_name,omitempty"`
	DNSNames   []string  `json:"dns_names,omitempty"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
	LastLoad   time.Time `json:"last_load"`
}

// CertStore extends CertSwapper with source-keyed entries, an SNI index and
// a read-only inventory. NewSwapper returns a CertStore.
type CertStore interface {
	CertSwapper
	// SetEntries atomically replaces the full entry set
	SetEntries([]*Entry)
	// Entries returns read-only metadata for the current entry set
	Entries() []EntryInfo
}

// ValidatePair parses and validates a PEM-encoded certificate chain and
// private key as a coherent pair, returning the certificate with its leaf
// parsed. Both file and in-memory sources flow through this validation.
func ValidatePair(certPEM, keyPEM []byte) (tls.Certificate, error) {
	// X509KeyPair parses the chain, verifies the key matches the leaf, and
	// populates Leaf
	return tls.X509KeyPair(certPEM, keyPEM)
}

// NewEntry returns an Entry for the provided certificate with its content
// hash and load time populated
func NewEntry(key, sourceKind string, cert tls.Certificate) *Entry {
	return &Entry{
		Key:         key,
		SourceKind:  sourceKind,
		Certificate: cert,
		ContentHash: hashChain(cert),
		LastLoad:    time.Now(),
	}
}

func hashChain(cert tls.Certificate) string {
	h := sha256.New()
	for _, der := range cert.Certificate {
		h.Write(der)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// storeSnapshot is the immutable state swapped into the store; GetCert only
// ever loads a snapshot, so the handshake path is lock-free.
type storeSnapshot struct {
	entries  []*Entry
	certs    []*tls.Certificate
	exact    map[string]*tls.Certificate
	wildcard map[string]*tls.Certificate
}

// certStore implements CertStore
type certStore struct {
	snapshot atomic.Pointer[storeSnapshot]
}

// NewStore returns a new CertStore populated with the provided entries
func NewStore(entries []*Entry) CertStore {
	s := &certStore{}
	s.SetEntries(entries)
	return s
}

// GetCert returns the best-matching certificate for the provided clientHello.
// Selection order: exact SNI match, wildcard SNI match, linear
// SupportsCertificate scan (covers no-SNI clients and IP SANs), then the
// first certificate.
func (s *certStore) GetCert(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	snap := s.snapshot.Load()
	if snap == nil || len(snap.certs) == 0 {
		return nil, ErrNoCertificates
	}
	if len(snap.certs) == 1 {
		// There's only one choice, so no point doing any work.
		return snap.certs[0], nil
	}
	if name := normalizeSNI(clientHello.ServerName); name != "" {
		if cert, ok := snap.exact[name]; ok {
			return cert, nil
		}
		if i := strings.IndexByte(name, '.'); i > 0 {
			if cert, ok := snap.wildcard[name[i+1:]]; ok {
				return cert, nil
			}
		}
	}
	for _, cert := range snap.certs {
		if err := clientHello.SupportsCertificate(cert); err == nil {
			return cert, nil
		}
	}
	// If nothing matches, return the first certificate.
	return snap.certs[0], nil
}

// SetCerts safely updates the certs list for the subject store. Certificates
// set this way carry no source identity and are keyed by content hash.
func (s *certStore) SetCerts(certs []tls.Certificate) {
	entries := make([]*Entry, len(certs))
	for i, cert := range certs {
		if cert.Leaf == nil && len(cert.Certificate) > 0 {
			// tolerated: an unparsable leaf only bypasses the SNI index
			cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
		}
		entries[i] = NewEntry("", SourceKindConfig, cert)
		entries[i].Key = SourceKindConfig + ":" + entries[i].ContentHash
	}
	s.SetEntries(entries)
}

// SetEntries atomically replaces the full entry set, deduplicating entries
// with identical content and rebuilding the SNI index
func (s *certStore) SetEntries(entries []*Entry) {
	snap := &storeSnapshot{
		entries:  make([]*Entry, 0, len(entries)),
		certs:    make([]*tls.Certificate, 0, len(entries)),
		exact:    make(map[string]*tls.Certificate),
		wildcard: make(map[string]*tls.Certificate),
	}
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e == nil || seen[e.ContentHash] {
			continue
		}
		seen[e.ContentHash] = true
		snap.entries = append(snap.entries, e)
		cert := &e.Certificate
		snap.certs = append(snap.certs, cert)
		indexCert(snap, cert)
	}
	s.snapshot.Store(snap)
}

// indexCert adds the cert's SANs (and legacy CN fallback) to the SNI index;
// on collision, the first-indexed certificate wins, matching the ordering
// semantics of the linear scan.
func indexCert(snap *storeSnapshot, cert *tls.Certificate) {
	if cert.Leaf == nil {
		return
	}
	names := cert.Leaf.DNSNames
	if len(names) == 0 && cert.Leaf.Subject.CommonName != "" {
		names = []string{cert.Leaf.Subject.CommonName}
	}
	for _, name := range names {
		name = strings.ToLower(name)
		if base, ok := strings.CutPrefix(name, "*."); ok {
			if _, exists := snap.wildcard[base]; !exists {
				snap.wildcard[base] = cert
			}
			continue
		}
		if _, exists := snap.exact[name]; !exists {
			snap.exact[name] = cert
		}
	}
}

// Entries returns read-only metadata for the current entry set
func (s *certStore) Entries() []EntryInfo {
	snap := s.snapshot.Load()
	if snap == nil {
		return nil
	}
	out := make([]EntryInfo, len(snap.entries))
	for i, e := range snap.entries {
		info := EntryInfo{
			Key:        e.Key,
			SourceKind: e.SourceKind,
			LastLoad:   e.LastLoad,
		}
		if leaf := e.Certificate.Leaf; leaf != nil {
			info.CommonName = leaf.Subject.CommonName
			info.DNSNames = leaf.DNSNames
			info.NotBefore = leaf.NotBefore
			info.NotAfter = leaf.NotAfter
		}
		out[i] = info
	}
	return out
}

func normalizeSNI(serverName string) string {
	return strings.ToLower(strings.TrimSuffix(serverName, "."))
}
