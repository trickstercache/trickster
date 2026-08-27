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
	"errors"
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

func TestRetentionCountZeroKeepsAllArchives(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 4
	o.RetentionCount = 0
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "aaaa")
	mustWrite(t, w, "bbbb")
	mustWrite(t, w, "cccc")
	w.Close()
	if s := readFile(t, o.Filename); s != "cccc" {
		t.Errorf("unexpected live content: %q", s)
	}
	if s := readFile(t, o.Filename+".1"); s != "bbbb" {
		t.Errorf("unexpected .1 content: %q", s)
	}
	if s := readFile(t, o.Filename+".2"); s != "aaaa" {
		t.Errorf("unexpected .2 content: %q", s)
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

func TestCompressedAgePruning(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 4
	o.RetentionAge = time.Hour
	o.Compress = true
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "aaaa")
	w.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	mustWrite(t, w, "bbbb")
	w.Close()
	if fileExists(o.Filename+".1") || fileExists(o.Filename+".1.gz") {
		t.Error("expected compressed aged archive to be pruned")
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
	if err = w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(o.Filename); err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return time.Now().Add(2 * pathCheckInterval) }
	mustWrite(t, w, "bbbb")
	w.Close()
	if s := readFile(t, o.Filename); s != "bbbb" {
		t.Errorf("unexpected content after reopen: %q", s)
	}
}

func TestRotationDoesNotWaitForCompression(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 4
	o.RetentionCount = 3
	o.Compress = true
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	w.compress = func(path string) {
		startedOnce.Do(func() { close(started) })
		<-release
		compressArchive(path)
	}
	mustWrite(t, w, "aaaa")
	mustWrite(t, w, "bbbb")
	<-started
	done := make(chan error, 1)
	go func() {
		_, err := w.Write([]byte("cccc"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Millisecond):
		t.Fatal("rotation blocked on background compression")
	}
	close(release)
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if s := readFile(t, o.Filename); s != "cccc" {
		t.Errorf("unexpected live content: %q", s)
	}
	if s := readGzipFile(t, o.Filename+".1.gz"); s != "bbbb" {
		t.Errorf("unexpected .1 content: %q", s)
	}
	if s := readGzipFile(t, o.Filename+".2.gz"); s != "aaaa" {
		t.Errorf("unexpected .2 content: %q", s)
	}
}

func TestBufferedWriteFlush(t *testing.T) {
	o := testOptions(t)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "buffered")
	if s := readFile(t, o.Filename); s != "" {
		t.Fatalf("write bypassed buffer: %q", s)
	}
	if err = w.Flush(); err != nil {
		t.Fatal(err)
	}
	if s := readFile(t, o.Filename); s != "buffered" {
		t.Errorf("unexpected flushed content: %q", s)
	}
	w.Close()
}

func TestBufferedWriteThresholdAndTimer(t *testing.T) {
	t.Run("threshold", func(t *testing.T) {
		o := testOptions(t)
		w, err := NewWriter(o)
		if err != nil {
			t.Fatal(err)
		}
		payload := strings.Repeat("x", writeBufferSize)
		mustWrite(t, w, payload)
		deadline := time.Now().Add(bufferFlushInterval)
		for time.Now().Before(deadline) {
			if b, readErr := os.ReadFile(o.Filename); readErr == nil && string(b) == payload {
				w.Close()
				return
			}
			time.Sleep(time.Millisecond)
		}
		w.Close()
		t.Error("threshold buffer was not flushed promptly")
	})

	t.Run("timer", func(t *testing.T) {
		o := testOptions(t)
		w, err := NewWriter(o)
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, w, "timer")
		deadline := time.Now().Add(2 * bufferFlushInterval)
		for time.Now().Before(deadline) {
			if b, readErr := os.ReadFile(o.Filename); readErr == nil && string(b) == "timer" {
				w.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		w.Close()
		t.Error("buffer was not flushed by the timer")
	})
}

func TestBufferLimitDropsWholeWrite(t *testing.T) {
	o := testOptions(t)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	w.mtx.Lock()
	w.buf = append(w.buf, make([]byte, maxWriteBufferSize-1)...)
	w.mtx.Unlock()
	if n, writeErr := w.Write([]byte("line")); n != 0 || !errors.Is(writeErr, ErrBufferFull) {
		t.Fatalf("write = %d, %v; want 0, ErrBufferFull", n, writeErr)
	}
	if w.DroppedLines() != 1 {
		t.Errorf("dropped lines = %d, want 1", w.DroppedLines())
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPartialWriteRetryDoesNotDuplicate(t *testing.T) {
	o := testOptions(t)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	first := true
	w.write = func(f *os.File, p []byte) (int, error) {
		if first {
			first = false
			n, writeErr := f.Write(p[:2])
			if writeErr != nil {
				return n, writeErr
			}
			return n, errors.New("injected partial write")
		}
		return f.Write(p)
	}
	mustWrite(t, w, "abcdef")
	if err = w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, o.Filename); got != "abcdef" {
		t.Errorf("partial retry content = %q, want abcdef", got)
	}
}

func TestFlushReopensRetainedBuffer(t *testing.T) {
	o := testOptions(t)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, w, "retained")
	dir := filepath.Dir(o.Filename)
	movedDir := dir + ".moved"
	first := true
	w.write = func(*os.File, []byte) (int, error) {
		if first {
			first = false
			if renameErr := os.Rename(dir, movedDir); renameErr != nil {
				t.Fatal(renameErr)
			}
			if writeErr := os.WriteFile(dir, []byte("blocker"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		return 0, errors.New("injected write failure")
	}
	if err = w.Flush(); err == nil {
		t.Fatal("expected reopen failure")
	}
	if removeErr := os.Remove(dir); removeErr != nil {
		t.Fatal(removeErr)
	}
	if renameErr := os.Rename(movedDir, dir); renameErr != nil {
		t.Fatal(renameErr)
	}
	w.write = func(f *os.File, p []byte) (int, error) { return f.Write(p) }
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, o.Filename); got != "retained" {
		t.Errorf("recovered content = %q, want retained", got)
	}
}

func TestIntervalEpochSurvivesRestart(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 0
	o.Interval = time.Hour
	start := time.Now().Add(-2 * time.Hour)
	w, err := NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return start }
	mustWrite(t, w, "old")
	w.Close()

	w, err = NewWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	w.now = time.Now
	mustWrite(t, w, "new")
	w.Close()
	if s := readFile(t, o.Filename); s != "new" {
		t.Errorf("unexpected live content: %q", s)
	}
	if s := readFile(t, o.Filename+".1"); s != "old" {
		t.Errorf("unexpected rotated content: %q", s)
	}
}

func readGzipFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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

func TestReconfigureUpdatesSharedWriter(t *testing.T) {
	o := testOptions(t)
	h1, err := GetWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	o2 := o.Clone()
	o2.MaxSizeBytes = 42
	if err = Reconfigure(o2); err != nil {
		t.Fatal(err)
	}
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
