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

// Package blob defines what a disk cache provider must implement to store
// framed objects, and the cache client that frames objects on its behalf
package blob

import (
	"errors"
	"io"
)

// ErrNoMeta is returned when a Store is asked for a metadata file it does not hold
var ErrNoMeta = errors.New("metadata file not found")

// Blob is one stored frame, open for reading until it is closed
type Blob interface {
	io.ReaderAt
	io.Closer
	// Size returns the length of the frame in bytes
	Size() int64
}

// ScanFunc inspects one stored frame during a Scan and reports whether the Store
// should remove it. The Blob is valid only until the func returns.
type ScanFunc func(b Blob) (remove bool)

// Store is the storage a disk cache provider supplies. It persists and returns whole
// frames by key, and knows nothing of what a frame holds.
type Store interface {
	Connect() error
	Close() error
	// Put writes hdr, meta and body as the single frame stored at key, replacing any other.
	// The Store must not retain the slices once Put returns.
	Put(key string, hdr, meta, body []byte) error
	// Open returns the frame stored at key, or cache.ErrKNF when there is none
	Open(key string) (Blob, error)
	// Delete removes the frames stored at keys. A key with no frame is not an error.
	Delete(keys ...string) error
	// DeleteIf removes the frame stored at key if fn, given that frame, says to. No Put of
	// the key comes between fn and the removal, so what fn saw is what is removed.
	DeleteIf(key string, fn ScanFunc) error
	// Scan passes up to limit frames to fn, in the Store's order, from past the position
	// after names, or the start. It returns where to resume, and whether it reached the end.
	Scan(after string, limit int, fn ScanFunc) (next string, done bool, err error)
	// Streamable reports whether a Blob may stay open while a slow reader consumes it
	Streamable() bool
}

// MetaStore is an optional Store capability for holding a cache's own named metadata
// files, such as an index, apart from the objects it caches
type MetaStore interface {
	// CreateMeta returns a writer for the named file, which replaces any file
	// of that name only once the writer is closed without error
	CreateMeta(name string) (io.WriteCloser, error)
	// AppendMeta adds p to the end of the named file, creating it when absent
	AppendMeta(name string, p []byte) error
	// OpenMeta returns a reader for the named file, or ErrNoMeta when there is none
	OpenMeta(name string) (io.ReadCloser, error)
	// RemoveMeta removes the named files. A name with no file is not an error.
	RemoveMeta(names ...string) error
	// ListMeta returns the names of the files held, in lexical order
	ListMeta() ([]string, error)
}

// FreeSpacer is an optional Store capability for reporting the space left on its medium
type FreeSpacer interface {
	// FreeBytes returns how many more bytes the Store's medium can hold
	FreeBytes() (int64, error)
}
