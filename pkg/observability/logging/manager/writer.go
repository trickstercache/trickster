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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Writer is a rotating, retention-managed log file writer. The live file is
// opened lazily on first Write. Implements io.WriteCloser.
type Writer struct {
	opts          Options
	mtx           sync.Mutex
	archiveMtx    sync.Mutex
	f             *os.File
	fileInfo      os.FileInfo
	buf           []byte
	size          int64
	openedAt      time.Time
	nextPathCheck time.Time
	flushTimer    *time.Timer
	flushActive   bool
	pendingSeq    uint64
	droppedLines  uint64
	wg            sync.WaitGroup
	millPending   bool
	millAgain     bool
	epochPending  bool
	epochAgain    bool
	closed        bool
	now           func() time.Time
	compress      func(string)
	write         func(*os.File, []byte) (int, error)
}

const (
	pathCheckInterval   = time.Second
	bufferFlushInterval = time.Second
	writeBufferSize     = 64 * 1024
	maxWriteBufferSize  = 4 * writeBufferSize
	rotationEpochSuffix = ".rotation"
	pendingMarker       = ".pending."
)

// NewWriter returns a Writer for the provided Options
func NewWriter(o *Options) (*Writer, error) {
	if o == nil || o.Filename == "" {
		return nil, ErrNoFilename
	}
	opts := *o
	if opts.FileMode == 0 {
		opts.FileMode = DefaultFileMode
	}
	w := &Writer{
		opts: opts, now: time.Now, compress: compressArchive,
		buf: make([]byte, 0, writeBufferSize),
		write: func(f *os.File, p []byte) (int, error) {
			return f.Write(p)
		},
	}
	if len(listPending(opts.Filename)) > 0 {
		w.requestMill()
	}
	return w, nil
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
	if err := w.ensureCurrentFile(); err != nil {
		return 0, err
	}
	if w.shouldRotate(int64(len(p))) {
		if err := w.flushLocked(); err != nil {
			return 0, err
		}
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	if len(p) > maxWriteBufferSize-len(w.buf) {
		w.droppedLines++
		w.scheduleFlush(0)
		return 0, ErrBufferFull
	}
	wasEmpty := len(w.buf) == 0
	w.buf = append(w.buf, p...)
	w.size += int64(len(p))
	if len(w.buf) >= writeBufferSize {
		w.scheduleFlush(0)
		return len(p), nil
	}
	if wasEmpty {
		w.scheduleFlush(bufferFlushInterval)
	}
	return len(p), nil
}

// DroppedLines returns writes rejected because the bounded buffer was full.
func (w *Writer) DroppedLines() uint64 {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	return w.droppedLines
}

// Flush writes buffered log data to the live file.
func (w *Writer) Flush() error {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.closed {
		return os.ErrClosed
	}
	return w.flushLocked()
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
	if err := w.flushLocked(); err != nil {
		return err
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
	w.stopFlushTimer()
	err := w.flushLocked()
	if w.f != nil {
		if closeErr := w.f.Close(); err == nil {
			err = closeErr
		}
		w.f = nil
		w.fileInfo = nil
	}
	w.mtx.Unlock()
	w.wg.Wait()
	return err
}

func (w *Writer) scheduleFlush(delay time.Duration) {
	if w.closed || len(w.buf) == 0 {
		return
	}
	if w.flushActive {
		if delay == 0 {
			w.flushTimer.Reset(0)
		}
		return
	}
	w.flushActive = true
	if w.flushTimer != nil {
		w.flushTimer.Reset(delay)
		return
	}
	w.flushTimer = time.AfterFunc(delay, func() {
		w.mtx.Lock()
		if !w.flushActive {
			w.mtx.Unlock()
			return
		}
		w.flushActive = false
		if !w.closed {
			_ = w.flushLocked()
		}
		w.mtx.Unlock()
	})
}

func (w *Writer) stopFlushTimer() {
	if w.flushTimer != nil {
		w.flushTimer.Stop()
	}
	w.flushActive = false
}

func (w *Writer) flushLocked() error {
	w.stopFlushTimer()
	if len(w.buf) == 0 {
		return nil
	}
	if w.f == nil {
		if err := w.open(); err != nil {
			w.scheduleFlush(bufferFlushInterval)
			return err
		}
	}
	err := w.writeBufferOnce()
	if err != nil && len(w.buf) > 0 {
		_ = w.f.Close()
		w.f = nil
		w.fileInfo = nil
		if openErr := w.open(); openErr != nil {
			w.scheduleFlush(bufferFlushInterval)
			return openErr
		}
		err = w.writeBufferOnce()
	}
	if len(w.buf) > 0 {
		w.scheduleFlush(bufferFlushInterval)
	}
	return err
}

func (w *Writer) writeBufferOnce() error {
	n, err := w.write(w.f, w.buf)
	if n > len(w.buf) {
		n = len(w.buf)
	}
	if n > 0 {
		copy(w.buf, w.buf[n:])
		w.buf = w.buf[:len(w.buf)-n]
	}
	if err == nil && len(w.buf) > 0 {
		return io.ErrShortWrite
	}
	return err
}

func (w *Writer) shouldRotate(incoming int64) bool {
	if w.opts.MaxSizeBytes > 0 && w.size+incoming > w.opts.MaxSizeBytes {
		return w.size > 0
	}
	if w.opts.Interval > 0 && w.now().Sub(w.openedAt) >= w.opts.Interval {
		return w.size > 0
	}
	return false
}

func (w *Writer) open() error {
	return w.openAt(time.Time{})
}

func (w *Writer) openAt(epoch time.Time) error {
	if err := os.MkdirAll(filepath.Dir(w.opts.Filename), DefaultDirMode); err != nil {
		return err
	}
	f, err := os.OpenFile(w.opts.Filename,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, w.opts.FileMode)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	now := w.now()
	w.f = f
	w.fileInfo = fi
	w.size = fi.Size() + int64(len(w.buf))
	if epoch.IsZero() {
		epoch = readRotationEpoch(w.opts.Filename)
	}
	if epoch.IsZero() {
		epoch = now
		if fi.Size() > 0 {
			epoch = fi.ModTime()
		}
	}
	w.openedAt = epoch
	w.nextPathCheck = now.Add(pathCheckInterval)
	w.requestEpochWrite()
	return nil
}

func (w *Writer) requestEpochWrite() {
	if w.epochPending {
		w.epochAgain = true
		return
	}
	w.epochPending = true
	w.wg.Add(1)
	go w.epochWriteRun()
}

func (w *Writer) epochWriteRun() {
	defer w.wg.Done()
	for {
		w.mtx.Lock()
		filename, epoch, mode := w.opts.Filename, w.openedAt, w.opts.FileMode
		w.mtx.Unlock()
		writeRotationEpoch(filename, epoch, mode)
		w.mtx.Lock()
		if !w.epochAgain {
			w.epochPending = false
			w.mtx.Unlock()
			return
		}
		w.epochAgain = false
		w.mtx.Unlock()
	}
}

func (w *Writer) ensureCurrentFile() error {
	now := w.now()
	if now.Before(w.nextPathCheck) {
		return nil
	}
	w.nextPathCheck = now.Add(pathCheckInterval)
	fi, err := os.Stat(w.opts.Filename)
	if err == nil && w.fileInfo != nil && os.SameFile(w.fileInfo, fi) {
		w.size = fi.Size() + int64(len(w.buf))
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	w.fileInfo = nil
	_ = os.Remove(rotationEpochName(w.opts.Filename))
	return w.openAt(now)
}

func (w *Writer) rotate() error {
	now := w.now()
	if w.f != nil {
		if err := w.f.Close(); err != nil {
			return err
		}
		w.f = nil
		w.fileInfo = nil
	}
	w.pendingSeq++
	pending := pendingName(w.opts.Filename, now, w.pendingSeq)
	err := os.Rename(w.opts.Filename, pending)
	if err != nil && !os.IsNotExist(err) {
		_ = w.open()
		return err
	}
	if err := w.openAt(now); err != nil {
		return err
	}
	w.requestMill()
	return nil
}

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

func (w *Writer) mill() {
	w.mtx.Lock()
	opts := w.opts
	w.mtx.Unlock()
	w.archiveMtx.Lock()
	defer w.archiveMtx.Unlock()
	for _, pending := range listPending(opts.Filename) {
		shiftArchives(opts.Filename, opts.RetentionCount)
		_ = os.Rename(pending.path, archiveName(opts.Filename, 1, false))
	}
	archives := listArchives(opts.Filename)
	if opts.Compress {
		for _, a := range archives {
			if !a.compressed {
				w.compress(a.path)
			}
		}
		archives = listArchives(opts.Filename)
	}
	now := w.now()
	for _, a := range archives {
		if opts.RetentionCount > 0 && a.n > opts.RetentionCount {
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

func shiftArchives(filename string, retentionCount int) {
	if retentionCount > 0 {
		removeArchive(filename, retentionCount)
		for n := retentionCount - 1; n >= 1; n-- {
			shiftArchive(filename, n)
		}
		return
	}
	maxArchive := 0
	for _, a := range listArchives(filename) {
		if a.n > maxArchive {
			maxArchive = a.n
		}
	}
	for n := maxArchive; n >= 1; n-- {
		shiftArchive(filename, n)
	}
}

func shiftArchive(filename string, n int) {
	if p := archiveName(filename, n, true); fileExists(p) {
		_ = os.Rename(p, archiveName(filename, n+1, true))
	}
	if p := archiveName(filename, n, false); fileExists(p) {
		_ = os.Rename(p, archiveName(filename, n+1, false))
	}
}

type pendingArchive struct {
	path    string
	name    string
	modTime time.Time
}

func pendingName(filename string, now time.Time, seq uint64) string {
	return filename + pendingMarker + strconv.FormatInt(now.UnixNano(), 10) + "." +
		strconv.FormatUint(seq, 10)
}

func listPending(filename string) []pendingArchive {
	dir := filepath.Dir(filename)
	prefix := filepath.Base(filename) + pendingMarker
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]pendingArchive, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, pendingArchive{
			path: filepath.Join(dir, entry.Name()), name: entry.Name(),
			modTime: info.ModTime(),
		})
	}
	slices.SortFunc(out, func(a, b pendingArchive) int {
		if order := a.modTime.Compare(b.modTime); order != 0 {
			return order
		}
		return strings.Compare(a.name, b.name)
	})
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

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

func rotationEpochName(filename string) string {
	return filename + rotationEpochSuffix
}

func readRotationEpoch(filename string) time.Time {
	b, err := os.ReadFile(rotationEpochName(filename))
	if err != nil {
		return time.Time{}
	}
	nanos, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

func writeRotationEpoch(filename string, epoch time.Time, mode os.FileMode) {
	b := strconv.AppendInt(nil, epoch.UnixNano(), 10)
	_ = os.WriteFile(rotationEpochName(filename), b, mode)
}
