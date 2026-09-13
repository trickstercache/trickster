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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

const testPollInterval = 10 * time.Millisecond

type loadRecorder struct {
	mtx     sync.Mutex
	entries []*Entry
}

func (r *loadRecorder) onLoad(e *Entry) {
	r.mtx.Lock()
	r.entries = append(r.entries, e)
	r.mtx.Unlock()
}

func (r *loadRecorder) count() int {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return len(r.entries)
}

func (r *loadRecorder) last() *Entry {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if len(r.entries) == 0 {
		return nil
	}
	return r.entries[len(r.entries)-1]
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return condition()
}

func writePair(t *testing.T, certPath, keyPath string, names ...string) {
	t.Helper()
	k, c, err := tlstest.GetTestKeyAndCertWithNames(names...)
	if err != nil {
		t.Fatal(err)
	}
	// key first so a reader never observes a new cert with the old key
	if err := os.WriteFile(keyPath, k, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, c, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFileSetWatcherDetectsRotation(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writePair(t, certPath, keyPath, "one.example.com")

	rec := &loadRecorder{}
	w := NewFileSetWatcher(FileSet{CertPath: certPath, KeyPath: keyPath},
		testPollInterval, rec.onLoad)
	if w == nil {
		t.Fatal("expected a watcher")
	}
	defer w.Close()

	// the initial load is synchronous
	if rec.count() != 1 {
		t.Fatalf("expected 1 initial load, got %d", rec.count())
	}
	first := rec.last()
	if first.Key != (FileSet{CertPath: certPath, KeyPath: keyPath}).Key() ||
		first.SourceKind != SourceKindFile {
		t.Errorf("unexpected initial entry: %+v", first)
	}

	// rotate in place via atomic rename, as certbot-style tooling does
	nextCert, nextKey := filepath.Join(dir, "next.crt"), filepath.Join(dir, "next.key")
	writePair(t, nextCert, nextKey, "two.example.com")
	if err := os.Rename(nextKey, keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(nextCert, certPath); err != nil {
		t.Fatal(err)
	}

	if !waitFor(t, 3*time.Second, func() bool { return rec.count() >= 2 }) {
		t.Fatal("rotation not detected")
	}
	last := rec.last()
	if last.ContentHash == first.ContentHash {
		t.Error("expected a different certificate after rotation")
	}
	if last.Certificate.Leaf.DNSNames[0] != "two.example.com" {
		t.Errorf("unexpected rotated cert SAN: %s", last.Certificate.Leaf.DNSNames[0])
	}
}

// TestFileSetWatcherPartialRotation verifies pair coherence: when the cert
// file changes before the key file, the mismatched pair is never delivered;
// the swap fires only once both halves are in place.
func TestFileSetWatcherPartialRotation(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writePair(t, certPath, keyPath, "one.example.com")

	rec := &loadRecorder{}
	w := NewFileSetWatcher(FileSet{CertPath: certPath, KeyPath: keyPath},
		testPollInterval, rec.onLoad)
	defer w.Close()

	k2, c2, err := tlstest.GetTestKeyAndCertWithNames("two.example.com")
	if err != nil {
		t.Fatal(err)
	}
	// write only the new cert; the key on disk still matches the old cert
	if err := os.WriteFile(certPath, c2, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * testPollInterval)
	if rec.count() != 1 {
		t.Fatalf("mid-rotation partial state was delivered; loads = %d", rec.count())
	}
	// complete the rotation
	if err := os.WriteFile(keyPath, k2, 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool { return rec.count() >= 2 }) {
		t.Fatal("completed rotation not detected")
	}
	if rec.last().Certificate.Leaf.DNSNames[0] != "two.example.com" {
		t.Error("expected the completed rotation's certificate")
	}
}

// TestFileSetWatcherCABundleChange verifies a change to only the CA bundle
// member of the set re-fires the load callback.
func TestFileSetWatcherCABundleChange(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	caPath := filepath.Join(dir, "ca.crt")
	writePair(t, certPath, keyPath, "one.example.com")
	if err := os.WriteFile(caPath, []byte("ca one\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := &loadRecorder{}
	w := NewFileSetWatcher(
		FileSet{CertPath: certPath, KeyPath: keyPath, CAPaths: []string{caPath}},
		testPollInterval, rec.onLoad)
	defer w.Close()

	if err := os.WriteFile(caPath, []byte("ca two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool { return rec.count() >= 2 }) {
		t.Fatal("CA-bundle-only change not detected")
	}
}

func TestFileSetWatcherDisabled(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	writePair(t, certPath, keyPath, "one.example.com")
	if w := NewFileSetWatcher(FileSet{CertPath: certPath, KeyPath: keyPath},
		0, nil); w != nil {
		t.Error("expected nil watcher for disabled interval")
	}
}
