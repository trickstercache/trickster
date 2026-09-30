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

// Package bbolt is the bbolt implementation of the Trickster Cache,
// which keeps every cached object in a single database file
package bbolt

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"

	"go.etcd.io/bbolt"
)

const (
	dbFileMode  = 0o644
	openTimeout = time.Second
	// maxPooledFrame is the largest frame buffer kept for reuse, so one large object
	// does not hold its memory long after it was written
	maxPooledFrame = 1 << 20
)

var errClosed = errors.New("bbolt cache is not connected")

var (
	_ blob.Store     = (*Store)(nil)
	_ blob.MetaStore = (*Store)(nil)
)

var framePool = sync.Pool{New: func() any { return new([]byte) }}

// Store is a blob.Store that keeps each frame as a value in a bbolt bucket
type Store struct {
	Name   string
	Config *options.Options
	dbh    *bbolt.DB
	bucket []byte
	meta   []byte
}

// NewStore returns a Store for the named cache. A fileName or bucketName that is not
// empty takes the place of the configured one.
func NewStore(cacheName, fileName, bucketName string, opts *options.Options) *Store {
	if opts == nil {
		opts = options.New()
	}
	if bucketName != "" {
		opts.BBolt.Bucket = bucketName
	}
	if fileName != "" {
		opts.BBolt.Filename = fileName
	}
	return &Store{Name: cacheName, Config: opts}
}

// Close closes the database file
func (s *Store) Close() error {
	if s.dbh == nil {
		return nil
	}
	return s.dbh.Close()
}

// Connect opens the database file, creating it and its buckets when they are absent
func (s *Store) Connect() error {
	dbh, err := bbolt.Open(s.Config.BBolt.Filename, dbFileMode, &bbolt.Options{Timeout: openTimeout})
	if err != nil {
		return err
	}
	s.bucket = []byte(s.Config.BBolt.Bucket)
	s.meta = []byte(s.Config.BBolt.Bucket + metaBucketSuffix)
	err = dbh.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{s.bucket, s.meta} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("create bucket: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		dbh.Close()
		return err
	}
	s.dbh = dbh
	return nil
}

// Streamable reports false: a Blob holds a read transaction open, and one held for
// long keeps the database from reclaiming space and can stall its writers
func (*Store) Streamable() bool {
	return false
}

func (s *Store) update(fn func(tx *bbolt.Tx) error) error {
	if s.dbh == nil {
		return errClosed
	}
	return s.dbh.Update(fn)
}

// Put writes the frame for key as one value
func (s *Store) Put(key string, hdr, meta, body []byte) error {
	// bbolt takes a value whole, and has copied it into its pages once the update returns
	bp := framePool.Get().(*[]byte)
	frame := append(append(append((*bp)[:0], hdr...), meta...), body...)
	err := s.update(func(tx *bbolt.Tx) error {
		return tx.Bucket(s.bucket).Put([]byte(key), frame)
	})
	if cap(frame) <= maxPooledFrame {
		*bp = frame[:0]
	}
	framePool.Put(bp)
	return err
}

// the read transaction stays open until the Blob is closed; a Blob of a Scan has none of its
// own
type txBlob struct {
	tx    *bbolt.Tx
	value []byte
}

func (b *txBlob) Size() int64 {
	return int64(len(b.value))
}

func (b *txBlob) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(b.value)) {
		return 0, io.EOF
	}
	n := copy(p, b.value[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (b *txBlob) Close() error {
	if b.tx == nil {
		return nil
	}
	return b.tx.Rollback()
}

// Open returns the frame stored for key. Its bytes are read from the database's
// own memory, so the Blob must be closed promptly.
func (s *Store) Open(key string) (blob.Blob, error) {
	if s.dbh == nil {
		return nil, errClosed
	}
	tx, err := s.dbh.Begin(false)
	if err != nil {
		return nil, err
	}
	value := tx.Bucket(s.bucket).Get([]byte(key))
	if value == nil {
		tx.Rollback()
		return nil, cache.ErrKNF
	}
	return &txBlob{tx: tx, value: value}, nil
}

// Delete removes the frames stored for keys in one transaction
func (s *Store) Delete(keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return s.update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(s.bucket)
		for _, key := range keys {
			if err := b.Delete([]byte(key)); err != nil {
				return err
			}
		}
		return nil
	})
}

// the length and these leading bytes, which hold the header's time and checksum, tell one
// frame from another put in its place
const fingerprintLen = 64

type fingerprint struct {
	key  string
	size int
	head []byte
}

func fingerprintOf(key string, value []byte) fingerprint {
	return fingerprint{key: key, size: len(value), head: bytes.Clone(value[:min(len(value), fingerprintLen)])}
}

func (f *fingerprint) matches(value []byte) bool {
	return value != nil && len(value) == f.size && bytes.Equal(value[:len(f.head)], f.head)
}

// removes each frame only while it is still the one that was fingerprinted
func (s *Store) deleteMatching(prints []fingerprint) error {
	if len(prints) == 0 {
		return nil
	}
	return s.update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(s.bucket)
		for i := range prints {
			key := []byte(prints[i].key)
			if prints[i].matches(b.Get(key)) {
				if err := b.Delete(key); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// DeleteIf removes the frame for key if fn says to of it, within one transaction
func (s *Store) DeleteIf(key string, fn blob.ScanFunc) error {
	return s.update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(s.bucket)
		k := []byte(key)
		value := b.Get(k)
		if value == nil || !fn(&txBlob{value: value}) {
			return nil
		}
		return b.Delete(k)
	})
}

// Scan visits frames in key order within one short read transaction, and then
// removes those the visit marked, if they are still the same, in a transaction of their own
func (s *Store) Scan(after string, limit int, fn blob.ScanFunc) (string, bool, error) {
	if s.dbh == nil {
		return after, false, errClosed
	}
	next, done := after, true
	var removals []fingerprint
	err := s.dbh.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket(s.bucket).Cursor()
		from := []byte(after)
		var n int
		for k, v := c.Seek(from); k != nil; k, v = c.Next() {
			if v == nil || (after != "" && bytes.Equal(k, from)) {
				// a nested bucket is no frame, and the frame at the position was visited already
				continue
			}
			if n == limit {
				done = false
				return nil
			}
			n++
			next = string(k)
			if fn(&txBlob{value: v}) {
				removals = append(removals, fingerprintOf(next, v))
			}
		}
		return nil
	})
	if err == nil {
		err = s.deleteMatching(removals)
	}
	return next, done && err == nil, err
}
