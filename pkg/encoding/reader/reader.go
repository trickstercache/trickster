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

package reader

import (
	"bytes"
	"io"
	"sync/atomic"
)

// Resetter is an interface that requires a Reader Reset function
type Resetter interface {
	Reset(r io.Reader) error
}

// BaseReader is an interface that requires a BaseReader function
type BaseReader interface {
	BaseReader() io.Reader
}

// ReadCloserResetter is a combination of a traditional ReadCloser and a Resetter
type ReadCloserResetter interface {
	Resetter
	io.ReadCloser
}

type nopReadCloserResetter struct {
}

// readCloserResetter implements ReadCloserResetter
type readCloserResetter struct {
	io.Reader
	closeCnt atomic.Int32
	resetter Resetter
}

// Close will call the underlying Readr's Close if it is castable to a ReadCloser,
// otherwise it will noop
func (rc *readCloserResetter) Close() error {
	// This gracefully handles when Close is called more than once and ensures
	// only the first caller, even in a burst, is able to proceed.
	if rc.closeCnt.Add(1) != 1 {
		return nil
	}
	// if the underlying io.Reader is actually itself an io.ReadCloser, call
	// the parent close instead of noop
	if closer, ok := rc.Reader.(io.Closer); ok {
		return closer.Close()
	}
	// getting to this point means the Readr is not a Closer, so we noop here
	return nil
}

func (rc *readCloserResetter) Reset(r io.Reader) error {
	if rc.resetter != nil {
		return rc.resetter.Reset(r)
	}
	return nil
}

func (rc *readCloserResetter) BaseReader() io.Reader {
	return rc.Reader
}

// NewReadCloserResetter returns a ReadCloserResetter using the provided Reader
func NewReadCloserResetter(r io.Reader) ReadCloserResetter {
	rc := &readCloserResetter{Reader: r}
	if rst, ok := r.(Resetter); ok {
		rc.resetter = rst
	}
	return rc
}

// NewReadCloserResetterBytes returns a ReadCloserResetter using the provided byte slice
func NewReadCloserResetterBytes(b []byte) ReadCloserResetter {
	rc := &readCloserResetter{Reader: bytes.NewReader(b)}
	return rc
}
