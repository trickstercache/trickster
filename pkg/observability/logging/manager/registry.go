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
	"io"
	"sync"
)

// registry deduplicates Writers by absolute filename so that multiple
// consumers of the same log file share one rotation manager
var registry = struct {
	sync.Mutex
	writers map[string]*regEntry
}{writers: make(map[string]*regEntry)}

type regEntry struct {
	w       *Writer
	opts    Options
	refs    int
	closing bool
	cond    *sync.Cond
}

// Handle is a reference-counted handle to a shared Writer; Close releases
// the reference, closing the underlying Writer on last release
type Handle struct {
	w    *Writer
	key  string
	once sync.Once
}

func (h *Handle) Write(p []byte) (int, error) {
	return h.w.Write(p)
}

// Flush writes buffered data to the underlying log file.
func (h *Handle) Flush() error { return h.w.Flush() }

// DroppedLines returns writes rejected because the shared buffer was full.
func (h *Handle) DroppedLines() uint64 { return h.w.DroppedLines() }

// Rotate forces an immediate rotation of the underlying Writer
func (h *Handle) Rotate() error {
	return h.w.Rotate()
}

// Filename returns the path to the underlying live log file
func (h *Handle) Filename() string {
	return h.w.Filename()
}

// Close releases this handle's reference to the shared Writer. The Writer
// is closed when the last handle is released. Close is idempotent.
func (h *Handle) Close() error {
	var err error
	h.once.Do(func() {
		registry.Lock()
		e, ok := registry.writers[h.key]
		if ok && e.w == h.w {
			e.refs--
			if e.refs < 1 {
				e.closing = true
			} else {
				ok = false
			}
		}
		registry.Unlock()
		if ok {
			err = h.w.Close()
			registry.Lock()
			if registry.writers[h.key] == e {
				delete(registry.writers, h.key)
			}
			e.cond.Broadcast()
			registry.Unlock()
		}
	})
	return err
}

var _ io.WriteCloser = &Handle{}

// GetWriter returns a Handle to the shared Writer for the filename in the
// provided Options, creating the Writer if one does not exist
func GetWriter(o *Options) (*Handle, error) {
	opts, err := normalizeOptions(o)
	if err != nil {
		return nil, err
	}
	key := opts.Filename
	registry.Lock()
	defer registry.Unlock()
	e, ok := registry.writers[key]
	for ok && e.closing {
		e.cond.Wait()
		e, ok = registry.writers[key]
	}
	if !ok {
		w, err := NewWriter(&opts)
		if err != nil {
			return nil, err
		}
		e = &regEntry{w: w, opts: opts}
		e.cond = sync.NewCond(&registry.Mutex)
		registry.writers[key] = e
	} else if !optionsEqual(e.opts, opts) {
		return nil, ErrConflictingOptions
	}
	e.refs++
	return &Handle{w: e.w, key: key}, nil
}

// Reconfigure updates an existing shared Writer during a validated reload.
// It is a no-op when the filename does not currently have a Writer.
func Reconfigure(o *Options) error {
	opts, err := normalizeOptions(o)
	if err != nil {
		return err
	}
	registry.Lock()
	defer registry.Unlock()
	e, ok := registry.writers[opts.Filename]
	for ok && e.closing {
		e.cond.Wait()
		e, ok = registry.writers[opts.Filename]
	}
	if !ok {
		return nil
	}
	e.w.SetOptions(&opts)
	e.opts = opts
	return nil
}
