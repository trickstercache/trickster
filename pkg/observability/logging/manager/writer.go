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
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Writer is a rotating, retention-managed log file writer. The live file is
// opened lazily on first Write. Implements io.WriteCloser.
type Writer struct {
	opts        Options
	mtx         sync.Mutex
	f           *os.File
	size        int64
	openedAt    time.Time
	wg          sync.WaitGroup
	millPending bool
	millAgain   bool
	closed      bool
	now         func() time.Time
}

// NewWriter returns a Writer for the provided Options
func NewWriter(o *Options) (*Writer, error) {
	if o == nil || o.Filename == "" {
		return nil, ErrNoFilename
	}
	opts := *o
	if opts.FileMode == 0 {
		opts.FileMode = DefaultFileMode
	}
	return &Writer{opts: opts, now: time.Now}, nil
}

// Filename returns the path to the live log file
func (w *Writer) Filename() string {
	return w.opts.Filename
}

// SetOptions updates the Writer's rotation, retention and compression
// settings in place; the filename cannot be changed
func (w *Writer) SetOptions(o *Options) {
	if o == nil {
		return
	}
	w.mtx.Lock()
	w.opts.MaxSizeBytes = o.MaxSizeBytes
	w.opts.Interval = o.Interval
	w.opts.RetentionCount = o.RetentionCount
	w.opts.RetentionAge = o.RetentionAge
	w.opts.Compress = o.Compress
	if o.FileMode != 0 {
		w.opts.FileMode = o.FileMode
	}
	w.mtx.Unlock()
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.shouldRotate(int64(len(p))) {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	if err != nil {
		// the live file may have been removed or invalidated externally
		// (e.g., by logrotate); reopen and retry the write once
		w.f.Close()
		w.f = nil
		if err = w.open(); err != nil {
			return n, err
		}
		n, err = w.f.Write(p)
	}
	w.size += int64(n)
	return n, err
}

// Rotate forces an immediate rotation of the live log file
func (w *Writer) Rotate() error {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.closed {
		return os.ErrClosed
	}
	if w.f == nil {
		if err := w.open(); err != nil {
			return err
		}
	}
	return w.rotate()
}

// Close closes the live file and waits for any background archive
// maintenance to complete. Close is idempotent.
func (w *Writer) Close() error {
	w.mtx.Lock()
	if w.closed {
		w.mtx.Unlock()
		return nil
	}
	w.closed = true
	var err error
	if w.f != nil {
		err = w.f.Close()
		w.f = nil
	}
	w.mtx.Unlock()
	w.wg.Wait()
	return err
}

// shouldRotate is called with the mutex held
func (w *Writer) shouldRotate(incoming int64) bool {
	if w.opts.MaxSizeBytes > 0 && w.size+incoming > w.opts.MaxSizeBytes {
		return w.size > 0
	}
	if w.opts.Interval > 0 && w.now().Sub(w.openedAt) >= w.opts.Interval {
		return w.size > 0
	}
	return false
}

// open is called with the mutex held
func (w *Writer) open() error {
	if err := os.MkdirAll(filepath.Dir(w.opts.Filename), DefaultDirMode); err != nil {
		return err
	}
	f, err := os.OpenFile(w.opts.Filename,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, w.opts.FileMode)
	if err != nil {
		return err
	}
	w.f = f
	w.size = 0
	w.openedAt = w.now()
	if fi, err2 := f.Stat(); err2 == nil {
		w.size = fi.Size()
		if fi.Size() > 0 {
			w.openedAt = fi.ModTime()
		}
	}
	return nil
}

// rotate is called with the mutex held. It closes the live file, shifts the
// numbered archives, reopens a fresh live file, and requests background
// compression/pruning.
func (w *Writer) rotate() error {
	if w.f != nil {
		w.f.Close()
		w.f = nil
	}
	if w.opts.RetentionCount < 1 {
		os.Remove(w.opts.Filename)
	} else {
		removeArchive(w.opts.Filename, w.opts.RetentionCount)
		for n := w.opts.RetentionCount - 1; n >= 1; n-- {
			shiftArchive(w.opts.Filename, n)
		}
		err := os.Rename(w.opts.Filename, archiveName(w.opts.Filename, 1, false))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := w.open(); err != nil {
		return err
	}
	w.requestMill()
	return nil
}

// requestMill is called with the mutex held
func (w *Writer) requestMill() {
	if w.millPending {
		w.millAgain = true
		return
	}
	w.millPending = true
	w.wg.Add(1)
	go w.millRun()
}

func (w *Writer) millRun() {
	defer w.wg.Done()
	for {
		w.mill()
		w.mtx.Lock()
		if !w.millAgain {
			w.millPending = false
			w.mtx.Unlock()
			return
		}
		w.millAgain = false
		w.mtx.Unlock()
	}
}

// mill compresses uncompressed archives and prunes archives beyond the
// retention count or age. Errors are ignored: the writer cannot usefully
// report failures of its own log maintenance.
func (w *Writer) mill() {
	w.mtx.Lock()
	opts := w.opts
	w.mtx.Unlock()
	archives := listArchives(opts.Filename)
	if opts.Compress {
		for _, a := range archives {
			if !a.compressed {
				compressArchive(a.path)
			}
		}
	}
	now := w.now()
	for _, a := range archives {
		if a.n > opts.RetentionCount {
			os.Remove(a.path)
			continue
		}
		if opts.RetentionAge > 0 {
			if fi, err := os.Stat(a.path); err == nil &&
				now.Sub(fi.ModTime()) > opts.RetentionAge {
				os.Remove(a.path)
			}
		}
	}
}

type archive struct {
	path       string
	n          int
	compressed bool
}

func archiveName(filename string, n int, compressed bool) string {
	s := filename + "." + strconv.Itoa(n)
	if compressed {
		s += ".gz"
	}
	return s
}

func removeArchive(filename string, n int) {
	os.Remove(archiveName(filename, n, false))
	os.Remove(archiveName(filename, n, true))
}

// shiftArchive best-effort renames archive n to n+1, preferring the
// compressed form
func shiftArchive(filename string, n int) {
	if p := archiveName(filename, n, true); fileExists(p) {
		_ = os.Rename(p, archiveName(filename, n+1, true))
		return
	}
	if p := archiveName(filename, n, false); fileExists(p) {
		_ = os.Rename(p, archiveName(filename, n+1, false))
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// listArchives returns the numbered archives for the provided live filename
func listArchives(filename string) []archive {
	dir := filepath.Dir(filename)
	base := filepath.Base(filename) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]archive, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), base) {
			continue
		}
		suffix, compressed := strings.CutSuffix(e.Name()[len(base):], ".gz")
		n, err := strconv.Atoi(suffix)
		if err != nil || n < 1 {
			continue
		}
		out = append(out, archive{
			path:       filepath.Join(dir, e.Name()),
			n:          n,
			compressed: compressed,
		})
	}
	return out
}

// compressArchive gzips the file at path to path.gz, preserving its
// modification time, and removes the original on success
func compressArchive(path string) {
	src, err := os.Open(path)
	if err != nil {
		return
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return
	}
	dstPath := path + ".gz"
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode())
	if err != nil {
		return
	}
	gz := gzip.NewWriter(dst)
	_, err = io.Copy(gz, src)
	if err == nil {
		err = gz.Close()
	} else {
		_ = gz.Close()
	}
	if err2 := dst.Close(); err == nil {
		err = err2
	}
	if err != nil {
		os.Remove(dstPath)
		return
	}
	_ = os.Chtimes(dstPath, fi.ModTime(), fi.ModTime())
	os.Remove(path)
}
