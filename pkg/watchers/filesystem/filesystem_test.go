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
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

const testPollInterval = 10 * time.Millisecond

type changeRecorder struct {
	mtx      sync.Mutex
	accepted [][][]byte
	// reject causes OnChange to reject observations whose members are not
	// all identical, simulating a consumer's coherence validation
	reject bool
}

var errIncoherent = errors.New("file set members do not match")

func (r *changeRecorder) onChange(contents [][]byte) error {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if r.reject {
		for _, c := range contents[1:] {
			if !bytes.Equal(c, contents[0]) {
				return errIncoherent
			}
		}
	}
	r.accepted = append(r.accepted, contents)
	return nil
}

func (r *changeRecorder) count() int {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return len(r.accepted)
}

func (r *changeRecorder) last() [][]byte {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if len(r.accepted) == 0 {
		return nil
	}
	return r.accepted[len(r.accepted)-1]
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return condition()
}

func writeFiles(t *testing.T, content string, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func testPaths(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	writeFiles(t, "one", a, b)
	return dir, a, b
}

func TestNewValidation(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrNilOptions) {
		t.Errorf("expected ErrNilOptions, got %v", err)
	}
	if _, err := New(&Options{Interval: time.Second}); !errors.Is(err, ErrNoPaths) {
		t.Errorf("expected ErrNoPaths, got %v", err)
	}
	if _, err := New(&Options{Paths: []string{"x"}}); !errors.Is(err, ErrInvalidInterval) {
		t.Errorf("expected ErrInvalidInterval, got %v", err)
	}
	if _, err := StartNew(&Options{Paths: []string{"x"}}); !errors.Is(err, ErrInvalidInterval) {
		t.Errorf("expected ErrInvalidInterval from StartNew, got %v", err)
	}
}

// TestNewDoesNotStart verifies that New does not begin watching until Start
// is called, while StartNew delivers the initial content synchronously
func TestNewDoesNotStart(t *testing.T) {
	_, a, b := testPaths(t)
	rec := &changeRecorder{}
	w, err := New(&Options{Name: "test", Paths: []string{a, b},
		Interval: testPollInterval, OnChange: rec.onChange})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * testPollInterval)
	if rec.count() != 0 {
		t.Fatalf("watcher delivered before Start; count = %d", rec.count())
	}
	w.Start()
	defer w.Close()
	if rec.count() != 1 {
		t.Fatalf("expected 1 initial delivery after Start, got %d", rec.count())
	}
	w.Start() // Start on a running watcher is a no-op
	if rec.count() != 1 {
		t.Fatalf("second Start re-delivered; count = %d", rec.count())
	}
}

func TestDetectsChange(t *testing.T) {
	_, a, b := testPaths(t)
	rec := &changeRecorder{}
	w, err := StartNew(&Options{Name: "test", Paths: []string{a, b},
		Interval: testPollInterval, OnChange: rec.onChange})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if rec.count() != 1 {
		t.Fatalf("expected 1 initial delivery, got %d", rec.count())
	}
	// change via atomic rename, as rotation tooling does
	dir := filepath.Dir(a)
	next := filepath.Join(dir, "next.txt")
	writeFiles(t, "two", next, b)
	if err := os.Rename(next, a); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool { return rec.count() >= 2 }) {
		t.Fatal("change not detected")
	}
	if string(rec.last()[0]) != "two" {
		t.Errorf("unexpected content delivered: %q", rec.last()[0])
	}
}

// TestRejectedChangeIsRetried verifies the OnChange rejection contract: a
// rejected observation (e.g. a consumer's mid-rotation coherence check) is
// not recorded as applied and is re-delivered once the content settles
func TestRejectedChangeIsRetried(t *testing.T) {
	_, a, b := testPaths(t)
	rec := &changeRecorder{reject: true}
	w, err := StartNew(&Options{Name: "test", Paths: []string{a, b},
		Interval: testPollInterval, OnChange: rec.onChange})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// write only half the set; the incoherent state must never be accepted
	writeFiles(t, "two", a)
	time.Sleep(10 * testPollInterval)
	if rec.count() != 1 {
		t.Fatalf("incoherent state was accepted; count = %d", rec.count())
	}
	// complete the change
	writeFiles(t, "two", b)
	if !waitFor(t, 3*time.Second, func() bool { return rec.count() >= 2 }) {
		t.Fatal("settled change not delivered")
	}
	if string(rec.last()[0]) != "two" || string(rec.last()[1]) != "two" {
		t.Errorf("unexpected content delivered: %q %q", rec.last()[0], rec.last()[1])
	}
}

// TestSymlinkSwap simulates the kubelet AtomicWriter layout used by
// Kubernetes secret and projected volumes, where file paths are symlinks
// through a ..data directory that is swapped atomically. Content comparison
// must see through the swap even though the top-level path's inode never
// changes.
func TestSymlinkSwap(t *testing.T) {
	dir := t.TempDir()
	dataDir1 := filepath.Join(dir, "..data_1")
	dataDir2 := filepath.Join(dir, "..data_2")
	for _, d := range []string{dataDir1, dataDir2} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles(t, "one", filepath.Join(dataDir1, "a.txt"))
	writeFiles(t, "two", filepath.Join(dataDir2, "a.txt"))
	dataLink := filepath.Join(dir, "..data")
	if err := os.Symlink(dataDir1, dataLink); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(dir, "a.txt")
	if err := os.Symlink(filepath.Join(dataLink, "a.txt"), a); err != nil {
		t.Fatal(err)
	}

	rec := &changeRecorder{}
	w, err := StartNew(&Options{Name: "test", Paths: []string{a},
		Interval: testPollInterval, OnChange: rec.onChange})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if rec.count() != 1 {
		t.Fatalf("expected 1 initial delivery, got %d", rec.count())
	}

	// atomic symlink swap: point ..data at the new payload via rename
	tmpLink := filepath.Join(dir, "..data_tmp")
	if err := os.Symlink(dataDir2, tmpLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpLink, dataLink); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool { return rec.count() >= 2 }) {
		t.Fatal("symlink-swap change not detected")
	}
	if string(rec.last()[0]) != "two" {
		t.Errorf("unexpected content delivered: %q", rec.last()[0])
	}
}

// TestDeletionAndRecovery verifies that deletion is a persistent read
// failure rather than a delivery, the loop survives past FailureThreshold,
// OnReadError is invoked, and watching resumes when the files return
func TestDeletionAndRecovery(t *testing.T) {
	_, a, b := testPaths(t)
	rec := &changeRecorder{}
	var readErrs int
	var readErrsMtx sync.Mutex
	w, err := StartNew(&Options{Name: "test", Paths: []string{a, b},
		Interval: testPollInterval, OnChange: rec.onChange,
		OnReadError: func(error) {
			readErrsMtx.Lock()
			readErrs++
			readErrsMtx.Unlock()
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	// enough ticks to exceed FailureThreshold; loop must survive
	time.Sleep(time.Duration(FailureThreshold+3) * testPollInterval)
	if rec.count() != 1 {
		t.Fatalf("deletion should not deliver; count = %d", rec.count())
	}
	readErrsMtx.Lock()
	sawErrs := readErrs
	readErrsMtx.Unlock()
	if sawErrs == 0 {
		t.Error("expected OnReadError invocations for the deleted file")
	}

	writeFiles(t, "two", a, b)
	if !waitFor(t, 3*time.Second, func() bool { return rec.count() >= 2 }) {
		t.Fatal("watcher did not recover after files returned")
	}
}

// TestEventDriven verifies the fsnotify fast path: with a poll interval far
// too long to explain detection, a change must still be picked up promptly
// via filesystem events. (On filesystems without event support the watcher
// silently degrades to poll-only, which the other tests cover.)
func TestEventDriven(t *testing.T) {
	_, a, b := testPaths(t)
	rec := &changeRecorder{}
	w, err := StartNew(&Options{Name: "test", Paths: []string{a, b},
		Interval: time.Hour, OnChange: rec.onChange})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.events == nil {
		t.Skip("fsnotify unavailable on this filesystem; poll-only covered elsewhere")
	}
	writeFiles(t, "two", a, b)
	if !waitFor(t, 5*time.Second, func() bool { return rec.count() >= 2 }) {
		t.Fatal("change not detected via filesystem events")
	}
}

// TestRestart verifies Close followed by Start: the restarted watcher
// delivers changes that occurred while it was stopped, keeps detecting
// subsequent changes, and does not re-deliver unchanged content
func TestRestart(t *testing.T) {
	_, a, b := testPaths(t)
	rec := &changeRecorder{}
	w, err := StartNew(&Options{Name: "test", Paths: []string{a, b},
		Interval: testPollInterval, OnChange: rec.onChange})
	if err != nil {
		t.Fatal(err)
	}
	if rec.count() != 1 {
		t.Fatalf("expected 1 initial delivery, got %d", rec.count())
	}

	w.Close()
	w.Close() // Close on a stopped watcher is a no-op

	// a restart with unchanged content must not re-deliver
	w.Start()
	if rec.count() != 1 {
		t.Fatalf("restart re-delivered unchanged content; count = %d", rec.count())
	}
	w.Close()

	// a change made while stopped is delivered by the next Start's initial check
	writeFiles(t, "two", a, b)
	time.Sleep(5 * testPollInterval)
	if rec.count() != 1 {
		t.Fatalf("stopped watcher delivered; count = %d", rec.count())
	}
	w.Start()
	if rec.count() != 2 || string(rec.last()[0]) != "two" {
		t.Fatalf("restart did not deliver the change made while stopped; count = %d", rec.count())
	}

	// the restarted watcher keeps detecting live changes
	writeFiles(t, "three", a, b)
	if !waitFor(t, 3*time.Second, func() bool { return rec.count() >= 3 }) {
		t.Fatal("restarted watcher did not detect a live change")
	}
	w.Close()
}

// TestDirectoryRecreationRearm verifies that when a watched directory is
// removed and recreated (dropping its fsnotify watch), the timed poll both
// keeps detection alive and re-arms the event watch
func TestDirectoryRecreationRearm(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "sub")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(dir, "a.txt")
	writeFiles(t, "one", a)

	rec := &changeRecorder{}
	w, err := StartNew(&Options{Name: "test", Paths: []string{a},
		Interval: testPollInterval, OnChange: rec.onChange})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if rec.count() != 1 {
		t.Fatalf("expected 1 initial delivery, got %d", rec.count())
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * testPollInterval)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, "two", a)
	if !waitFor(t, 3*time.Second, func() bool { return rec.count() >= 2 }) {
		t.Fatal("watcher did not recover after directory recreation")
	}
	if string(rec.last()[0]) != "two" {
		t.Errorf("unexpected content delivered: %q", rec.last()[0])
	}
}

// TestEventWatchUnavailableDirs verifies graceful poll-only degradation when
// no watched file's directory can be event-watched (e.g. it does not exist
// yet), and that polling delivers once the files appear
func TestEventWatchUnavailableDirs(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "missing", "a.txt")
	rec := &changeRecorder{}
	w, err := StartNew(&Options{Name: "test", Paths: []string{a},
		Interval: testPollInterval, OnChange: rec.onChange})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.events != nil {
		t.Error("expected poll-only mode when no directory can be event-watched")
	}
	if rec.count() != 0 {
		t.Fatalf("expected no delivery for unreadable set, got %d", rec.count())
	}
	if err := os.Mkdir(filepath.Dir(a), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, "one", a)
	if !waitFor(t, 3*time.Second, func() bool { return rec.count() >= 1 }) {
		t.Fatal("poll-only watcher did not deliver once the file appeared")
	}
}

func TestLifecycleNoLeaks(t *testing.T) {
	_, a, b := testPaths(t)
	before := runtime.NumGoroutine()
	ws := make([]*Watcher, 0, 8)
	for range 8 {
		w, err := StartNew(&Options{Name: "test", Paths: []string{a, b},
			Interval: testPollInterval})
		if err != nil {
			t.Fatal(err)
		}
		ws = append(ws, w)
	}
	// exercise a restart cycle on one of them before teardown
	ws[0].Close()
	ws[0].Start()
	for _, w := range ws {
		w.Close()
	}
	if !waitFor(t, 3*time.Second, func() bool {
		return runtime.NumGoroutine() <= before
	}) {
		t.Errorf("goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
	}
}
