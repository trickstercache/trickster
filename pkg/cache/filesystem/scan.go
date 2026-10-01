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

package filesystem

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
)

const (
	// tempMaxAge is how old a temporary file must be before it is taken for abandoned
	tempMaxAge = time.Hour
	// pruneBatch is how many entries of the cache path are read at a time when pruning it
	pruneBatch = 1024

	cursorSeparator = "/"
	cursorParts     = 3
)

func isShard(name string) bool {
	if len(name) != shardWidth {
		return false
	}
	for i := range len(name) {
		if c := name[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func readShards(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	shards := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && isShard(entry.Name()) {
			shards = append(shards, entry.Name())
		}
	}
	return shards, nil
}

// a cache of the earlier flat layout left its objects directly in the cache path, where no
// frame is kept now
func (s *Store) prune() error {
	d, err := os.Open(s.root)
	if err != nil {
		return err
	}
	defer d.Close()
	for {
		entries, err := d.ReadDir(pruneBatch)
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				os.Remove(filepath.Join(s.root, entry.Name()))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// a temporary file this old belongs to no write that is still running
func removeIfAbandoned(path string, entry fs.DirEntry) {
	if fi, err := entry.Info(); err == nil && time.Since(fi.ModTime()) > tempMaxAge {
		os.Remove(path)
	}
}

func (s *Store) visit(path string, fn blob.ScanFunc) {
	b, err := openBlob(path)
	if err != nil {
		return
	}
	defer b.Close()
	if fn(b) {
		// a frame put in its place since it was opened is not the one that was looked at
		_ = s.removeSame(path, b.File)
	}
}

// Scan visits the files holding frames, one directory after another in lexical order.
// A scan from the beginning first clears the cache path of what is not a frame.
func (s *Store) Scan(after string, limit int, fn blob.ScanFunc) (string, bool, error) {
	var from [cursorParts]string
	if parts := strings.Split(after, cursorSeparator); len(parts) == cursorParts {
		from = [cursorParts]string(parts)
	} else if err := s.prune(); err != nil {
		return after, false, err
	}
	next := after
	var n int
	outer, err := readShards(s.root)
	if err != nil {
		return next, false, err
	}
	for _, o := range outer {
		if o < from[0] {
			continue
		}
		inner, err := readShards(filepath.Join(s.root, o))
		if err != nil {
			return next, false, err
		}
		for _, i := range inner {
			if o == from[0] && i < from[1] {
				continue
			}
			dir := filepath.Join(s.root, o, i)
			entries, err := os.ReadDir(dir)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return next, false, err
			}
			for _, entry := range entries {
				name := entry.Name()
				if !entry.Type().IsRegular() || (o == from[0] && i == from[1] && name <= from[2]) {
					continue
				}
				path := filepath.Join(dir, name)
				if strings.HasPrefix(name, tempPrefix) {
					removeIfAbandoned(path, entry)
					continue
				}
				if n == limit {
					return next, false, nil
				}
				n++
				next = o + cursorSeparator + i + cursorSeparator + name
				s.visit(path, fn)
			}
		}
	}
	return next, true, nil
}
