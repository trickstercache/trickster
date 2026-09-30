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

package bbolt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/cache/blob"

	"go.etcd.io/bbolt"
)

const (
	metaBucketSuffix = ".meta"
	// a metadata file is kept as numbered segments, each keyed by the file's name,
	// this separator and the segment's number
	segmentSeparator = 0
	segmentNumberLen = 8
	segmentSize      = 1 << 20
)

// ErrInvalidMetaName is returned for a metadata file name that cannot key a segment
var ErrInvalidMetaName = errors.New("invalid metadata file name")

// every segment of a file is keyed by this prefix and its number
func segmentPrefix(name string) ([]byte, error) {
	if name == "" || strings.IndexByte(name, segmentSeparator) >= 0 {
		return nil, ErrInvalidMetaName
	}
	return append(append(make([]byte, 0, len(name)+1+segmentNumberLen), name...), segmentSeparator), nil
}

func segmentKey(prefix []byte, number uint64) []byte {
	return binary.BigEndian.AppendUint64(bytes.Clone(prefix), number)
}

func nextSegment(b *bbolt.Bucket, prefix []byte) uint64 {
	c := b.Cursor()
	// the position just past every segment the file could have
	k, _ := c.Seek(append(bytes.Clone(prefix[:len(prefix)-1]), segmentSeparator+1))
	if k == nil {
		k, _ = c.Last()
	} else {
		k, _ = c.Prev()
	}
	if !bytes.HasPrefix(k, prefix) || len(k) != len(prefix)+segmentNumberLen {
		return 0
	}
	return binary.BigEndian.Uint64(k[len(prefix):]) + 1
}

func removeSegments(b *bbolt.Bucket, prefix []byte) error {
	c := b.Cursor()
	for k, _ := c.Seek(prefix); bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		if err := c.Delete(); err != nil {
			return err
		}
	}
	return nil
}

// what is written is kept in memory until Close puts it in place in one transaction
type metaWriter struct {
	bytes.Buffer
	store  *Store
	prefix []byte
}

// Close replaces the file's segments with what was written, in one transaction
func (w *metaWriter) Close() error {
	return w.store.update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(w.store.meta)
		if err := removeSegments(b, w.prefix); err != nil {
			return err
		}
		content := w.Bytes()
		// a file with no content still has a segment, which is how it is known to exist
		for number := uint64(0); number == 0 || len(content) > 0; number++ {
			segment := content[:min(len(content), segmentSize)]
			content = content[len(segment):]
			if err := b.Put(segmentKey(w.prefix, number), segment); err != nil {
				return err
			}
		}
		return nil
	})
}

// CreateMeta returns a writer for the named metadata file, which takes its place once closed
func (s *Store) CreateMeta(name string) (io.WriteCloser, error) {
	prefix, err := segmentPrefix(name)
	if err != nil {
		return nil, err
	}
	return &metaWriter{store: s, prefix: prefix}, nil
}

// AppendMeta adds p to the named metadata file as its next segment
func (s *Store) AppendMeta(name string, p []byte) error {
	prefix, err := segmentPrefix(name)
	if err != nil {
		return err
	}
	return s.update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(s.meta)
		return b.Put(segmentKey(prefix, nextSegment(b, prefix)), p)
	})
}

// the read transaction stays open until the reader is closed
type metaReader struct {
	tx      *bbolt.Tx
	cursor  *bbolt.Cursor
	prefix  []byte
	segment []byte
}

func (r *metaReader) Read(p []byte) (int, error) {
	for len(r.segment) == 0 {
		k, v := r.cursor.Next()
		if !bytes.HasPrefix(k, r.prefix) {
			return 0, io.EOF
		}
		r.segment = v
	}
	n := copy(p, r.segment)
	r.segment = r.segment[n:]
	return n, nil
}

func (r *metaReader) Close() error {
	return r.tx.Rollback()
}

// OpenMeta returns a reader for the named metadata file
func (s *Store) OpenMeta(name string) (io.ReadCloser, error) {
	prefix, err := segmentPrefix(name)
	if err != nil {
		return nil, err
	}
	if s.dbh == nil {
		return nil, errClosed
	}
	tx, err := s.dbh.Begin(false)
	if err != nil {
		return nil, err
	}
	c := tx.Bucket(s.meta).Cursor()
	k, v := c.Seek(prefix)
	if !bytes.HasPrefix(k, prefix) {
		tx.Rollback()
		return nil, blob.ErrNoMeta
	}
	return &metaReader{tx: tx, cursor: c, prefix: prefix, segment: v}, nil
}

// RemoveMeta removes the named metadata files in one transaction
func (s *Store) RemoveMeta(names ...string) error {
	return s.update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(s.meta)
		for _, name := range names {
			prefix, err := segmentPrefix(name)
			if err == nil {
				err = removeSegments(b, prefix)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ListMeta returns the names of the metadata files held
func (s *Store) ListMeta() ([]string, error) {
	if s.dbh == nil {
		return nil, errClosed
	}
	var names []string
	err := s.dbh.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket(s.meta).Cursor()
		for k, _ := c.First(); k != nil; {
			end := bytes.IndexByte(k, segmentSeparator)
			if end < 0 {
				k, _ = c.Next()
				continue
			}
			names = append(names, string(k[:end]))
			// on to the first key past every segment of this file
			k, _ = c.Seek(append(bytes.Clone(k[:end]), segmentSeparator+1))
		}
		return nil
	})
	return names, err
}
