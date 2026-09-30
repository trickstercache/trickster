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

// Package filesystem is the filesystem implementation of the Trickster Cache,
// which keeps each cached object in a file of its own
package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/maphash"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/options"
)

const (
	// cache keys of these lengths that are all lowercase hex are digests already
	digestLenShort = 32
	digestLenLong  = 64
	// hashedBytes is how much of a key's hash names its file
	hashedBytes = 16
	// shardWidth is how many leading characters of a file's name make each directory level
	shardWidth = 2
	// shardPathLen is the length of the two directory levels and the separators after them
	shardPathLen = 2 * (shardWidth + 1)

	keyBufferSize  = 512
	pathBufferSize = 512
	// stripeCount is how many locks the files are spread over, each serializing the
	// replacement and removal of the files that hash to it
	stripeCount = 1024
	stripeMask  = stripeCount - 1

	fileMode = 0o600
	dirMode  = 0o750

	tempPrefix  = ".tmp."
	tempBase    = 36
	probePrefix = ".test."

	separator = filepath.Separator
)

var (
	_ blob.Store      = (*Store)(nil)
	_ blob.MetaStore  = (*Store)(nil)
	_ blob.FreeSpacer = (*Store)(nil)
)

// Store is a blob.Store that keeps each frame in its own file, named for its cache key
// and spread across two levels of directories
type Store struct {
	Name    string
	Config  *options.Options
	root    string
	temps   atomic.Uint64
	stripes [stripeCount]sync.Mutex
}

var stripeSeed = maphash.MakeSeed()

func (s *Store) stripe(path string) *sync.Mutex {
	return &s.stripes[maphash.String(stripeSeed, filepath.Base(path))&stripeMask]
}

// NewStore returns a Store for the named cache that keeps its files under the configured cache path
func NewStore(name string, config *options.Options) *Store {
	s := &Store{Name: name, Config: config, root: filepath.Clean(config.Filesystem.CachePath)}
	// #nosec G115 -- only the bit pattern matters, as a starting point for temporary file names
	s.temps.Store(uint64(time.Now().UnixNano()))
	return s
}

// Close does nothing, as the Store holds nothing open between calls
func (*Store) Close() error {
	return nil
}

// Connect creates the cache path when it is absent, and verifies it can be written to
func (s *Store) Connect() error {
	err := os.MkdirAll(s.root, dirMode)
	if err == nil {
		probe := filepath.Join(s.root, probePrefix+strconv.FormatInt(time.Now().UnixNano(), tempBase))
		if err = os.WriteFile(probe, nil, fileMode); err == nil {
			err = os.Remove(probe)
		}
	}
	if err != nil {
		return fmt.Errorf("[%s] directory is not writeable by trickster: %w", s.root, err)
	}
	return nil
}

// Streamable reports true: an open file is unaffected by a later write to its cache key
func (*Store) Streamable() bool {
	return true
}

// a lowercase hex digest can name a file as it is; anything else is hashed, so that no key can
// shape a path
func isDigest(key string) bool {
	if len(key) != digestLenShort && len(key) != digestLenLong {
		return false
	}
	for i := range len(key) {
		if c := key[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func appendName(dst []byte, key string) []byte {
	if isDigest(key) {
		return append(dst, key...)
	}
	var sum [sha256.Size]byte
	if len(key) <= keyBufferSize {
		var kb [keyBufferSize]byte
		sum = sha256.Sum256(append(kb[:0], key...))
	} else {
		sum = sha256.Sum256([]byte(key))
	}
	return hex.AppendEncode(dst, sum[:hashedBytes])
}

// returns the path and the length of its directory part
func (s *Store) appendPath(dst []byte, key string) ([]byte, int) {
	dst = append(append(dst, s.root...), separator)
	shard := len(dst)
	dst = appendName(append(dst, make([]byte, shardPathLen)...), key)
	name := dst[shard+shardPathLen:]
	copy(dst[shard:], name[:shardWidth])
	dst[shard+shardWidth] = separator
	copy(dst[shard+shardWidth+1:], name[shardWidth:2*shardWidth])
	dst[shard+shardPathLen-1] = separator
	return dst, shard + shardPathLen - 1
}

func (s *Store) appendTemp(dst []byte, dir string) []byte {
	dst = append(append(append(dst, dir...), separator), tempPrefix...)
	return strconv.AppendUint(dst, s.temps.Add(1), tempBase)
}

func createTemp(name, dir string) (*os.File, error) {
	// #nosec G304 -- the name is built from the configured cache path and a hash or digest
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
	if errors.Is(err, fs.ErrNotExist) {
		// directories are made as they are first needed, which keeps the usual write free of it
		if err = os.MkdirAll(dir, dirMode); err == nil {
			// #nosec G304 -- see above
			f, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
		}
	}
	return f, err
}

// Put writes the frame for key to a new file and renames it into place, so that a reader
// finds the earlier frame or this one, never part of either
func (s *Store) Put(key string, hdr, meta, body []byte) error {
	var pb, tb [pathBufferSize]byte
	p, dirLen := s.appendPath(pb[:0], key)
	path := string(p)
	temp := string(s.appendTemp(tb[:0], path[:dirLen]))
	f, err := createTemp(temp, path[:dirLen])
	if err != nil {
		return err
	}
	err = writeFrame(f, hdr, meta, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		mu := s.stripe(path)
		mu.Lock()
		err = os.Rename(temp, path)
		mu.Unlock()
	}
	if err != nil {
		os.Remove(temp)
	}
	return err
}

// a file put in place of the one opened as f is not the one that was looked at, and stays
func (s *Store) removeSame(path string, f *os.File) error {
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	mu := s.stripe(path)
	mu.Lock()
	defer mu.Unlock()
	current, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !os.SameFile(opened, current) {
		return nil
	}
	return os.Remove(path)
}

// DeleteIf removes the file for key if fn says to of the frame it holds
func (s *Store) DeleteIf(key string, fn blob.ScanFunc) error {
	var pb [pathBufferSize]byte
	p, _ := s.appendPath(pb[:0], key)
	path := string(p)
	b, err := openBlob(path)
	if err != nil {
		if errors.Is(err, cache.ErrKNF) {
			return nil
		}
		return err
	}
	defer b.Close()
	if !fn(b) {
		return nil
	}
	return s.removeSame(path, b.File)
}

// skip is how many bytes across the sections were written already
func writeSections(f *os.File, skip int, sections ...[]byte) error {
	for _, section := range sections {
		if skip >= len(section) {
			skip -= len(section)
			continue
		}
		if _, err := f.Write(section[skip:]); err != nil {
			return err
		}
		skip = 0
	}
	return nil
}

type fileBlob struct {
	*os.File
	size int64
}

func (b *fileBlob) Size() int64 {
	return b.size
}

func openBlob(path string) (*fileBlob, error) {
	// #nosec G304 -- the path is built from the configured cache path and a hash or digest
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, cache.ErrKNF
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &fileBlob{File: f, size: fi.Size()}, nil
}

// Open returns the file holding the frame for key
func (s *Store) Open(key string) (blob.Blob, error) {
	var pb [pathBufferSize]byte
	p, _ := s.appendPath(pb[:0], key)
	b, err := openBlob(string(p))
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Delete removes the files holding the frames for keys, and carries on past any
// it cannot remove to report them all together
func (s *Store) Delete(keys ...string) error {
	var errs []error
	var pb [pathBufferSize]byte
	for _, key := range keys {
		p, _ := s.appendPath(pb[:0], key)
		if err := os.Remove(string(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
