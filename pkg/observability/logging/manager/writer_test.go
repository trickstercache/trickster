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

package manager

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testOptions(t *testing.T) *Options {
	t.Helper()
	o := NewOptions()
	o.Filename = filepath.Join(t.TempDir(), "test.log")
	o.Compress = false
	return o
}

func mustWrite(t *testing.T, w *Writer, s string) {
	t.Helper()
	n, err := w.Write([]byte(s))
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if n != len(s) {
		t.Fatalf("expected %d bytes written, got %d", len(s), n)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s failed: %v", path, err)
	}
	return string(b)
}

func TestNewWriterErrors(t *testing.T) {
	if _, err := NewWriter(nil); err != ErrNoFilename {
		t.Errorf("expected ErrNoFilename, got %v", err)
	}
	if _, err := NewWriter(&Options{}); err != ErrNoFilename {
		t.Errorf("expected ErrNoFilename, got %v", err)
	}
}

func TestWriteAndAppend(t *testing.T) {
	o := testOptions(t)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "hello\n")
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	// a new writer on the same file should append, not truncate
	w2, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w2, "world\n")
	w2.Close()
	if s := readFile(t, o.Filename); s != "hello\nworld\n" {
		t.Errorf("unexpected content: %q", s)
	}
}

func TestSizeRotation(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 10
	o.RetentionCount = 2
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "1111111111") // fills the file exactly
	mustWrite(t, w, "2222222222") // triggers rotation to .1
	mustWrite(t, w, "3333333333") // triggers rotation, shifting .1 -> .2
	w.Close()
	if s := readFile(t, o.Filename); s != "3333333333" {
		t.Errorf("unexpected live content: %q", s)
	}
	if s := readFile(t, o.Filename+".1"); s != "2222222222" {
		t.Errorf("unexpected .1 content: %q", s)
	}
	if s := readFile(t, o.Filename+".2"); s != "1111111111" {
		t.Errorf("unexpected .2 content: %q", s)
	}
}

func TestRetentionCountPruning(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 4
	o.RetentionCount = 1
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"aaaa", "bbbb", "cccc"} {
		mustWrite(t, w, s)
	}
	w.Close()
	if s := readFile(t, o.Filename+".1"); s != "bbbb" {
		t.Errorf("unexpected .1 content: %q", s)
	}
	if fileExists(o.Filename + ".2") {
		t.Error("expected .2 to be pruned")
	}
}

func TestRetentionCountZeroDeletesOnRotate(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 4
	o.RetentionCount = 0
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "aaaa")
	mustWrite(t, w, "bbbb")
	w.Close()
	if s := readFile(t, o.Filename); s != "bbbb" {
		t.Errorf("unexpected live content: %q", s)
	}
	if fileExists(o.Filename + ".1") {
		t.Error("expected no archives with zero retention count")
	}
}

func TestCompression(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 4
	o.Compress = true
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "aaaa")
	mustWrite(t, w, "bbbb")
	w.Close() // waits for background compression
	gzPath := o.Filename + ".1.gz"
	if fileExists(o.Filename + ".1") {
		t.Error("expected uncompressed archive to be removed")
	}
	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("expected compressed archive: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "aaaa" {
		t.Errorf("unexpected archive content: %q", string(b))
	}
}

func TestCompressedArchiveShift(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 4
	o.Compress = true
	o.RetentionCount = 3
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "aaaa")
	mustWrite(t, w, "bbbb")
	w.Close()
	// reopen and rotate again; the compressed .1.gz must shift to .2.gz
	w2, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w2, "cccc")
	w2.Close()
	if !fileExists(o.Filename + ".1.gz") {
		t.Error("expected .1.gz to exist")
	}
	if !fileExists(o.Filename + ".2.gz") {
		t.Error("expected .2.gz to exist")
	}
}

func TestAgePruning(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 4
	o.RetentionAge = time.Hour
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "aaaa")
	// force rotation and simulate the archive being older than the max age
	w.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	mustWrite(t, w, "bbbb")
	w.Close()
	if fileExists(o.Filename+".1") || fileExists(o.Filename+".1.gz") {
		t.Error("expected aged archive to be pruned")
	}
}

func TestTimeRotation(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 0
	o.Interval = time.Hour
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "aaaa")
	mustWrite(t, w, "bbbb") // same window; no rotation
	w.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	mustWrite(t, w, "cccc")
	w.Close()
	if s := readFile(t, o.Filename); s != "cccc" {
		t.Errorf("unexpected live content: %q", s)
	}
	if s := readFile(t, o.Filename+".1"); s != "aaaabbbb" {
		t.Errorf("unexpected .1 content: %q", s)
	}
}

func TestForcedRotate(t *testing.T) {
	o := testOptions(t)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "aaaa")
	if err = w.Rotate(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "bbbb")
	w.Close()
	if s := readFile(t, o.Filename); s != "bbbb" {
		t.Errorf("unexpected live content: %q", s)
	}
	if s := readFile(t, o.Filename+".1"); s != "aaaa" {
		t.Errorf("unexpected .1 content: %q", s)
	}
	if err = w.Rotate(); err != os.ErrClosed {
		t.Errorf("expected ErrClosed, got %v", err)
	}
}

func TestWriteAfterClose(t *testing.T) {
	o := testOptions(t)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "aaaa")
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Errorf("expected idempotent close, got %v", err)
	}
	if _, err = w.Write([]byte("x")); err != os.ErrClosed {
		t.Errorf("expected ErrClosed, got %v", err)
	}
}

func TestReopenAfterExternalRemoval(t *testing.T) {
	o := testOptions(t)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "aaaa")
	// close the handle out from under the writer to force a write error
	w.f.Close()
	os.Remove(o.Filename)
	mustWrite(t, w, "bbbb")
	w.Close()
	if s := readFile(t, o.Filename); s != "bbbb" {
		t.Errorf("unexpected content after reopen: %q", s)
	}
}

func TestOpenFailure(t *testing.T) {
	o := NewOptions()
	// a path whose parent is a file cannot be created
	base := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(base, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	o.Filename = filepath.Join(base, "test.log")
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("x")); err == nil {
		t.Error("expected open error")
	}
	w.Close()
}

func TestConcurrentWrites(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 128
	o.RetentionCount = 100
	o.Compress = true
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	line := strings.Repeat("z", 31) + "\n"
	for range 8 {
		wg.Go(func() {
			for range 50 {
				w.Write([]byte(line))
			}
		})
	}
	wg.Wait()
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	// every written line must be present exactly once across live + archives
	var total int
	for _, a := range listArchives(o.Filename) {
		b, err := os.ReadFile(a.path)
		if err != nil {
			t.Fatal(err)
		}
		if a.compressed {
			zr, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			if b, err = io.ReadAll(zr); err != nil {
				t.Fatal(err)
			}
		}
		total += strings.Count(string(b), "\n")
	}
	total += strings.Count(readFile(t, o.Filename), "\n")
	if total != 400 {
		t.Errorf("expected 400 lines across all files, got %d", total)
	}
}

func TestListArchivesIgnoresUnrelated(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "test.log")
	for _, f := range []string{"test.log", "test.log.1", "test.log.2.gz",
		"test.log.bak", "test.log.0", "other.log.1"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0644)
	}
	os.Mkdir(filepath.Join(dir, "test.log.3"), 0755)
	archives := listArchives(name)
	if len(archives) != 2 {
		t.Fatalf("expected 2 archives, got %d", len(archives))
	}
}

func TestListArchivesMissingDir(t *testing.T) {
	if a := listArchives(filepath.Join(t.TempDir(), "nope", "x.log")); a != nil {
		t.Errorf("expected nil, got %v", a)
	}
}

func TestCompressArchiveErrors(t *testing.T) {
	// missing source is a no-op
	compressArchive(filepath.Join(t.TempDir(), "missing.log.1"))
	// an unwritable destination is a no-op that leaves the source in place
	dir := t.TempDir()
	src := filepath.Join(dir, "test.log.1")
	os.WriteFile(src, []byte("x"), 0644)
	os.Mkdir(src+".gz", 0755)
	compressArchive(src)
	if !fileExists(src) {
		t.Error("expected source to remain after failed compression")
	}
}

func TestRotateUnopened(t *testing.T) {
	o := testOptions(t)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Rotate(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if !fileExists(o.Filename) {
		t.Error("expected live file to exist after rotating an unopened writer")
	}
}

func TestDefaultFileModeApplied(t *testing.T) {
	w, err := NewWriter(&Options{
		Filename: filepath.Join(t.TempDir(), "test.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.opts.FileMode != DefaultFileMode {
		t.Errorf("expected default file mode, got %v", w.opts.FileMode)
	}
	w.Close()
}

func TestMillPrunesStrayArchives(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 4
	o.RetentionCount = 1
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	// simulate an archive left over from a larger retention setting
	os.WriteFile(o.Filename+".5", []byte("stale"), 0644)
	mustWrite(t, w, "aaaa")
	mustWrite(t, w, "bbbb")
	w.Close()
	if fileExists(o.Filename + ".5") {
		t.Error("expected stray archive beyond retention count to be pruned")
	}
	if !fileExists(o.Filename + ".1") {
		t.Error("expected .1 archive to be retained")
	}
}

func TestSetOptions(t *testing.T) {
	o := testOptions(t)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	w.SetOptions(nil) // no-op
	mustWrite(t, w, "aaaa")
	// lower the rotation threshold; the next write must rotate
	o2 := o.Clone()
	o2.MaxSizeBytes = 4
	o2.RetentionCount = 2
	w.SetOptions(o2)
	mustWrite(t, w, "bbbb")
	w.Close()
	if s := readFile(t, o.Filename+".1"); s != "aaaa" {
		t.Errorf("expected rotation under updated options, got %q", s)
	}
	if w.opts.FileMode != DefaultFileMode {
		t.Errorf("unexpected file mode: %v", w.opts.FileMode)
	}
}

func TestGetWriterUpdatesOptions(t *testing.T) {
	o := testOptions(t)
	h1, err := GetWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	o2 := o.Clone()
	o2.MaxSizeBytes = 42
	h2, err := GetWriter(o2)
	if err != nil {
		t.Fatal(err)
	}
	if h1.w != h2.w {
		t.Fatal("expected shared writer")
	}
	h1.w.mtx.Lock()
	size := h1.w.opts.MaxSizeBytes
	h1.w.mtx.Unlock()
	if size != 42 {
		t.Errorf("expected updated max size, got %d", size)
	}
	h1.Close()
	h2.Close()
}
