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

// Package blobtest provides an in-memory blob.Store for tests, and the
// conformance suite that every blob.Store implementation must pass
package blobtest

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
)

var (
	_ blob.Store      = (*MemStore)(nil)
	_ blob.MetaStore  = (*MemStore)(nil)
	_ blob.FreeSpacer = (*MemStore)(nil)
)

// MemStore is a blob.Store held in memory, whose calls can be made to fail
type MemStore struct {
	mu     sync.Mutex
	frames map[string][]byte
	meta   map[string][]byte

	// PutErr, OpenErr, DeleteErr, ScanErr and MetaErr, when set, fail the calls they name
	PutErr, OpenErr, DeleteErr, ScanErr, MetaErr error
	// ReadErr, when set, fails every read of an opened Blob
	ReadErr error
	// ReadErrAfter, when over zero, is how many reads of an opened Blob succeed before
	// the rest of them fail with ErrReadAfter
	ReadErrAfter int
	// Free is the space FreeBytes reports, and FreeErr fails it
	Free    int64
	FreeErr error
	// CanStream is what Streamable reports
	CanStream bool
	// OnDeleteIf, when set, runs inside DeleteIf once the frame is found and before it is judged
	OnDeleteIf func()
}

// NewMemStore returns an empty MemStore
func NewMemStore() *MemStore {
	return &MemStore{frames: make(map[string][]byte), meta: make(map[string][]byte)}
}

// ErrReadAfter is the error of a read that a MemStore's ReadErrAfter has failed
var ErrReadAfter = errors.New("read failed")

type memBlob struct {
	*bytes.Reader
	err error
	// reads is how many more reads succeed, when it is not negative
	reads int
}

func (b *memBlob) Close() error { return nil }

func (b *memBlob) Size() int64 { return b.Reader.Size() }

func (b *memBlob) ReadAt(p []byte, off int64) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if b.reads == 0 {
		return 0, ErrReadAfter
	}
	if b.reads > 0 {
		b.reads--
	}
	return b.Reader.ReadAt(p, off)
}

// Connect does nothing
func (*MemStore) Connect() error { return nil }

// Close does nothing
func (*MemStore) Close() error { return nil }

// Streamable reports CanStream
func (s *MemStore) Streamable() bool { return s.CanStream }

// FreeBytes reports Free
func (s *MemStore) FreeBytes() (int64, error) { return s.Free, s.FreeErr }

// Put stores a copy of the three sections as one frame
func (s *MemStore) Put(key string, hdr, meta, body []byte) error {
	if s.PutErr != nil {
		return s.PutErr
	}
	frame := slices.Concat(hdr, meta, body)
	s.mu.Lock()
	s.frames[key] = frame
	s.mu.Unlock()
	return nil
}

// SetFrame stores frame at key as it is, so a test can plant one that Put would never write
func (s *MemStore) SetFrame(key string, frame []byte) {
	s.mu.Lock()
	s.frames[key] = frame
	s.mu.Unlock()
}

// Frame returns the frame stored at key
func (s *MemStore) Frame(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	frame, ok := s.frames[key]
	return frame, ok
}

// Len returns how many frames are stored
func (s *MemStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

// Open returns the frame stored at key
func (s *MemStore) Open(key string) (blob.Blob, error) {
	if s.OpenErr != nil {
		return nil, s.OpenErr
	}
	s.mu.Lock()
	frame, ok := s.frames[key]
	s.mu.Unlock()
	if !ok {
		return nil, cache.ErrKNF
	}
	return s.blob(frame), nil
}

func (s *MemStore) blob(frame []byte) *memBlob {
	b := &memBlob{Reader: bytes.NewReader(frame), err: s.ReadErr, reads: -1}
	if s.ReadErrAfter > 0 {
		b.reads = s.ReadErrAfter
	}
	return b
}

// Delete removes the frames stored at keys
func (s *MemStore) Delete(keys ...string) error {
	if s.DeleteErr != nil {
		return s.DeleteErr
	}
	s.mu.Lock()
	for _, key := range keys {
		delete(s.frames, key)
	}
	s.mu.Unlock()
	return nil
}

// DeleteIf removes the frame at key if fn says to of it
func (s *MemStore) DeleteIf(key string, fn blob.ScanFunc) error {
	if s.DeleteErr != nil {
		return s.DeleteErr
	}
	frame, ok := s.Frame(key)
	if !ok {
		return nil
	}
	if s.OnDeleteIf != nil {
		s.OnDeleteIf()
	}
	remove := fn(s.blob(frame))
	s.mu.Lock()
	if current, held := s.frames[key]; remove && held && bytes.Equal(current, frame) {
		delete(s.frames, key)
	}
	s.mu.Unlock()
	return nil
}

// Scan visits frames in key order
func (s *MemStore) Scan(after string, limit int, fn blob.ScanFunc) (string, bool, error) {
	if s.ScanErr != nil {
		return after, false, s.ScanErr
	}
	s.mu.Lock()
	keys := make([]string, 0, len(s.frames))
	for key := range s.frames {
		if key > after {
			keys = append(keys, key)
		}
	}
	s.mu.Unlock()
	slices.Sort(keys)
	done := len(keys) <= limit
	if !done {
		keys = keys[:limit]
	}
	next := after
	for _, key := range keys {
		next = key
		frame, ok := s.Frame(key)
		if ok && fn(s.blob(frame)) {
			s.mu.Lock()
			if current, held := s.frames[key]; held && bytes.Equal(current, frame) {
				delete(s.frames, key)
			}
			s.mu.Unlock()
		}
	}
	return next, done, nil
}

type memMetaWriter struct {
	bytes.Buffer
	store *MemStore
	name  string
}

func (w *memMetaWriter) Close() error {
	w.store.mu.Lock()
	w.store.meta[w.name] = bytes.Clone(w.Bytes())
	w.store.mu.Unlock()
	return nil
}

// CreateMeta returns a writer whose content replaces the named file once closed
func (s *MemStore) CreateMeta(name string) (io.WriteCloser, error) {
	if s.MetaErr != nil {
		return nil, s.MetaErr
	}
	return &memMetaWriter{store: s, name: name}, nil
}

// AppendMeta adds p to the named file
func (s *MemStore) AppendMeta(name string, p []byte) error {
	if s.MetaErr != nil {
		return s.MetaErr
	}
	s.mu.Lock()
	s.meta[name] = append(s.meta[name], p...)
	s.mu.Unlock()
	return nil
}

// OpenMeta returns a reader over a copy of the named file
func (s *MemStore) OpenMeta(name string) (io.ReadCloser, error) {
	if s.MetaErr != nil {
		return nil, s.MetaErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.meta[name]
	if !ok {
		return nil, blob.ErrNoMeta
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(b))), nil
}

// SetMeta stores b as the named file
func (s *MemStore) SetMeta(name string, b []byte) {
	s.mu.Lock()
	s.meta[name] = b
	s.mu.Unlock()
}

// RemoveMeta removes the named files
func (s *MemStore) RemoveMeta(names ...string) error {
	if s.MetaErr != nil {
		return s.MetaErr
	}
	s.mu.Lock()
	for _, name := range names {
		delete(s.meta, name)
	}
	s.mu.Unlock()
	return nil
}

// ListMeta returns the names of the files held
func (s *MemStore) ListMeta() ([]string, error) {
	if s.MetaErr != nil {
		return nil, s.MetaErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.meta))
	for name := range s.meta {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}
