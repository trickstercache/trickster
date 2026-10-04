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
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
)

// the cache's own files, apart from the objects
const metaDir = "_meta"

// ErrInvalidMetaName is returned for a metadata file name that is not a plain file name
var ErrInvalidMetaName = errors.New("invalid metadata file name")

func (s *Store) metaPath(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) {
		return "", ErrInvalidMetaName
	}
	return filepath.Join(s.root, metaDir, name), nil
}

type metaWriter struct {
	*os.File
	temp, path string
	err        error
}

func (w *metaWriter) Write(p []byte) (int, error) {
	n, err := w.File.Write(p)
	if err != nil {
		w.err = err
	}
	return n, err
}

// Close renames what was written onto the file it is for, unless any of it failed to write
func (w *metaWriter) Close() error {
	err := w.File.Close()
	if w.err != nil {
		err = w.err
	}
	if err == nil {
		err = os.Rename(w.temp, w.path)
	}
	if err != nil {
		os.Remove(w.temp)
	}
	return err
}

// CreateMeta returns a writer for the named metadata file, which takes its place once closed
func (s *Store) CreateMeta(name string) (io.WriteCloser, error) {
	path, err := s.metaPath(name)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	temp := string(s.appendTemp(nil, dir))
	f, err := createTemp(temp, dir)
	if err != nil {
		return nil, err
	}
	return &metaWriter{File: f, temp: temp, path: path}, nil
}

// AppendMeta adds p to the end of the named metadata file
func (s *Store) AppendMeta(name string, p []byte) error {
	path, err := s.metaPath(name)
	if err != nil {
		return err
	}
	// #nosec G304 -- the path is the configured cache path and a validated plain file name
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fileMode)
	if errors.Is(err, fs.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(path), dirMode); err == nil {
			// #nosec G304 -- see above
			f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fileMode)
		}
	}
	if err != nil {
		return err
	}
	_, err = f.Write(p)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// OpenMeta returns a reader for the named metadata file
func (s *Store) OpenMeta(name string) (io.ReadCloser, error) {
	path, err := s.metaPath(name)
	if err != nil {
		return nil, err
	}
	// #nosec G304 -- the path is the configured cache path and a validated plain file name
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, blob.ErrNoMeta
		}
		return nil, err
	}
	return f, nil
}

// RemoveMeta removes the named metadata files, and carries on past any it cannot
// remove to report them all together
func (s *Store) RemoveMeta(names ...string) error {
	var errs []error
	for _, name := range names {
		path, err := s.metaPath(name)
		if err == nil {
			err = os.Remove(path)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ListMeta returns the names of the metadata files held
func (s *Store) ListMeta() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, metaDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && !strings.HasPrefix(entry.Name(), ".") {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}
