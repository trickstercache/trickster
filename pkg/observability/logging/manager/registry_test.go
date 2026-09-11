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
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestGetWriterErrors(t *testing.T) {
	if _, err := GetWriter(nil); err != ErrNoFilename {
		t.Errorf("expected ErrNoFilename, got %v", err)
	}
	if _, err := GetWriter(&Options{}); err != ErrNoFilename {
		t.Errorf("expected ErrNoFilename, got %v", err)
	}
}

func TestGetWriterSharing(t *testing.T) {
	o := testOptions(t)
	h1, err := GetWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := GetWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	if h1.w != h2.w {
		t.Error("expected handles to share one writer")
	}
	if h1.Filename() != o.Filename {
		t.Errorf("unexpected filename: %s", h1.Filename())
	}
	if _, err = h1.Write([]byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err = h1.Close(); err != nil {
		t.Fatal(err)
	}
	// writer must remain open while another handle holds a reference
	if _, err = h2.Write([]byte("two\n")); err != nil {
		t.Fatal(err)
	}
	if err = h2.Rotate(); err != nil {
		t.Fatal(err)
	}
	if err = h2.Close(); err != nil {
		t.Fatal(err)
	}
	if s := readFile(t, o.Filename+".1"); s != "one\ntwo\n" {
		t.Errorf("unexpected content: %q", s)
	}
	// last release must close the underlying writer
	if _, err = h2.w.Write([]byte("x")); err != os.ErrClosed {
		t.Errorf("expected ErrClosed, got %v", err)
	}
	registry.Lock()
	_, ok := registry.writers[mustAbs(t, o.Filename)]
	registry.Unlock()
	if ok {
		t.Error("expected registry entry to be removed")
	}
}

func TestHandleCloseIdempotent(t *testing.T) {
	o := testOptions(t)
	h1, err := GetWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := GetWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	// double-close of one handle must not release the other's reference
	h1.Close()
	h1.Close()
	if _, err = h2.Write([]byte("still open\n")); err != nil {
		t.Fatalf("writer closed prematurely: %v", err)
	}
	h2.Close()
}

func TestGetWriterAfterRelease(t *testing.T) {
	o := testOptions(t)
	h1, err := GetWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	h1.Write([]byte("first\n"))
	h1.Close()
	// a new handle after full release gets a fresh writer on the same file
	h2, err := GetWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	if h2.w == h1.w {
		t.Error("expected a new writer after full release")
	}
	h2.Write([]byte("second\n"))
	h2.Close()
	if s := readFile(t, o.Filename); s != "first\nsecond\n" {
		t.Errorf("unexpected content: %q", s)
	}
}

func TestGetWriterWaitsForPriorClose(t *testing.T) {
	o := testOptions(t)
	o.MaxSizeBytes = 4
	o.Compress = true
	h, err := GetWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.w.compress = func(path string) {
		once.Do(func() { close(started) })
		<-release
		compressArchive(path)
	}
	if _, err = h.Write([]byte("aaaa")); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Write([]byte("bbbb")); err != nil {
		t.Fatal(err)
	}
	<-started
	closed := make(chan error, 1)
	go func() { closed <- h.Close() }()
	// ensure Close has marked the entry closing before racing GetWriter against it
	deadline := time.Now().Add(5 * time.Second)
	for {
		registry.Lock()
		e, ok := registry.writers[h.key]
		closing := ok && e.closing
		registry.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close never marked the registry entry as closing")
		}
		time.Sleep(time.Millisecond)
	}
	acquired := make(chan *Handle, 1)
	go func() {
		next, getErr := GetWriter(o)
		if getErr != nil {
			t.Error(getErr)
		}
		acquired <- next
	}()
	select {
	case next := <-acquired:
		if next != nil {
			next.Close()
		}
		t.Fatal("writer was reacquired before the prior close completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err = <-closed; err != nil {
		t.Fatal(err)
	}
	next := <-acquired
	if next == nil || next.w == h.w {
		t.Fatal("expected a fresh writer after the prior close")
	}
	next.Close()
}

func TestGetWriterRejectsConflictingOptions(t *testing.T) {
	o := testOptions(t)
	h, err := GetWriter(o)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	conflict := o.Clone()
	conflict.RetentionCount++
	if _, err = GetWriter(conflict); !errors.Is(err, ErrConflictingOptions) {
		t.Fatalf("expected conflicting options error, got %v", err)
	}
	if err = Reconfigure(conflict); err != nil {
		t.Fatal(err)
	}
	h2, err := GetWriter(conflict)
	if err != nil {
		t.Fatalf("get after reconfiguration failed: %v", err)
	}
	h2.Close()
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	s, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
