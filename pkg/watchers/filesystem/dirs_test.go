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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

type eventRecorder struct {
	mtx       sync.Mutex
	paths     map[string]int
	lost      atomic.Int64
	intervals atomic.Int64
}

func newEventRecorder() *eventRecorder {
	return &eventRecorder{paths: make(map[string]int)}
}

func (r *eventRecorder) onEvent(path string) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.paths[path]++
}

func (r *eventRecorder) seen(path string) bool {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return r.paths[path] > 0
}

func (r *eventRecorder) options(interval time.Duration) *DirOptions {
	return &DirOptions{
		Name:       "test",
		Interval:   interval,
		OnEvent:    r.onEvent,
		OnLost:     func() { r.lost.Add(1) },
		OnInterval: func() { r.intervals.Add(1) },
	}
}

func TestNewDirWatcherErrors(t *testing.T) {
	r := newEventRecorder()
	if _, err := NewDirWatcher(nil); !errors.Is(err, ErrNilOptions) {
		t.Errorf("expected ErrNilOptions, got %v", err)
	}
	if _, err := NewDirWatcher(&DirOptions{Interval: time.Second}); !errors.Is(err, ErrNoEventHandler) {
		t.Errorf("expected ErrNoEventHandler, got %v", err)
	}
	if _, err := NewDirWatcher(&DirOptions{OnEvent: r.onEvent}); !errors.Is(err, ErrInvalidInterval) {
		t.Errorf("expected ErrInvalidInterval, got %v", err)
	}
}

func TestDirWatcherReportsChangedEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newEventRecorder()
	w, err := NewDirWatcher(r.options(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	defer w.Close()
	w.Start() // no-op while running
	if r.intervals.Load() != 1 {
		t.Errorf("expected one synchronous interval call at Start, got %d", r.intervals.Load())
	}
	w.Watch(dir)
	w.Watch(dir) // already watched
	if err := os.WriteFile(path, []byte("2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return r.seen(path) }) {
		t.Fatal("expected an event for the modified file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(created, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return r.seen(created) }) {
		t.Fatal("expected an event for the created file")
	}
}

func TestDirWatcherArmsDirsWatchedWhileStopped(t *testing.T) {
	dir := t.TempDir()
	r := newEventRecorder()
	w, err := NewDirWatcher(r.options(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	w.Close() // no-op while stopped
	w.Watch(dir)
	w.Watch(filepath.Join(dir, "missing")) // can't be armed; must not fail Start
	w.Start()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return r.seen(path) }) {
		t.Fatal("expected an event in a directory watched before Start")
	}
	w.Close()

	// a restart re-arms the set and reports the interval synchronously again
	before := r.intervals.Load()
	w.Start()
	defer w.Close()
	if r.intervals.Load() != before+1 {
		t.Error("expected a synchronous interval call on restart")
	}
	path2 := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(path2, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return r.seen(path2) }) {
		t.Fatal("expected an event after restart")
	}
}

func TestDirWatcherIntervalAndRearm(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "site")
	gone := filepath.Join(parent, "gone")
	for _, d := range []string{dir, gone} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	r := newEventRecorder()
	w, err := NewDirWatcher(r.options(testPollInterval))
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	defer w.Close()
	w.Watch(dir)
	w.Watch(gone)
	if !waitFor(t, 5*time.Second, func() bool { return r.intervals.Load() > 2 }) {
		t.Fatal("expected interval calls on the backstop cadence")
	}
	// the platform drops the watch with the directory; rearm then forgets it
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	forgotten := func() bool {
		w.mtx.RLock()
		defer w.mtx.RUnlock()
		_, ok := w.dirs[gone]
		return !ok
	}
	if !waitFor(t, 5*time.Second, forgotten) {
		t.Fatal("expected the removed directory to be forgotten")
	}
	// and a recreated directory is armed by the next Watch
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	w.Watch(gone)
	path := filepath.Join(gone, "a.txt")
	if err := os.WriteFile(path, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return r.seen(path) }) {
		t.Fatal("expected an event in the recreated directory")
	}
}

// armed returns the directories the platform is watching, which trail the watched set
func (w *DirWatcher) armed() []string {
	w.mtx.RLock()
	defer w.mtx.RUnlock()
	if w.events == nil {
		return nil
	}
	return w.events.WatchList()
}

func TestDirWatcherUnwatch(t *testing.T) {
	dir := t.TempDir()
	r := newEventRecorder()
	w, err := NewDirWatcher(r.options(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	w.Unwatch(dir) // not watched
	w.Watch(dir)
	w.Unwatch(dir) // while stopped, there is nothing armed to disarm
	if w.Watched() != 0 || len(w.stale) != 0 {
		t.Fatalf("expected no watched directories, got %d", w.Watched())
	}
	w.Start()
	defer w.Close()
	w.Watch(dir)
	if w.Watched() != 1 || len(w.armed()) != 1 {
		t.Fatalf("expected one watched directory, got %d", w.Watched())
	}
	// no longer wanted at once, and disarmed soon after, by the watch goroutine
	w.Unwatch(dir)
	if w.Watched() != 0 {
		t.Fatal("expected the directory to leave the watched set immediately")
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(w.armed()) == 0 }) {
		t.Fatal("expected the directory to be disarmed")
	}
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	// a second, still-watched directory proves events flow while the first stays silent
	other := t.TempDir()
	w.Watch(other)
	marker := filepath.Join(other, "b.txt")
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return r.seen(marker) }) {
		t.Fatal("expected an event in the watched directory")
	}
	if r.seen(path) {
		t.Error("expected no event from a disarmed directory")
	}
	// removing the directory first makes the platform drop the watch itself
	if err := os.RemoveAll(other); err != nil {
		t.Fatal(err)
	}
	w.Unwatch(other)
	if w.Watched() != 0 {
		t.Error("expected the removed directory to be forgotten")
	}
	if !waitFor(t, 5*time.Second, func() bool {
		w.mtx.RLock()
		defer w.mtx.RUnlock()
		return len(w.stale) == 0
	}) {
		t.Error("expected a directory the platform already dropped to be let go of all the same")
	}
}

func TestDirWatcherUnwatchNeverWaitsOnThePlatform(t *testing.T) {
	const dirs = 64
	r := newEventRecorder()
	// the watch goroutine is parked in the event handler, so that whatever happens meanwhile
	// can only have been done by the callers of Watch and Unwatch themselves
	parked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	o := r.options(time.Hour)
	o.OnEvent = func(path string) {
		once.Do(func() {
			close(parked)
			<-release
		})
		r.onEvent(path)
	}
	w, err := NewDirWatcher(o)
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	defer w.Close()
	parent := t.TempDir()
	paths := make([]string, dirs)
	for i := range paths {
		paths[i] = filepath.Join(parent, "d"+strconv.Itoa(i))
		if err := os.Mkdir(paths[i], 0o700); err != nil {
			t.Fatal(err)
		}
		w.Watch(paths[i])
	}
	if err := os.WriteFile(filepath.Join(paths[1], "park.txt"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	<-parked

	// and with the platform held against them besides, as it is while a call to it is under way,
	// unwatching every directory, and wanting one back, must still go straight through
	w.platform.Lock()
	unwatched := make(chan struct{})
	go func() {
		defer close(unwatched)
		for _, path := range paths {
			w.Unwatch(path)
		}
	}()
	select {
	case <-unwatched:
	case <-time.After(5 * time.Second):
		t.Fatal("Unwatch waited on the platform")
	}
	w.mtx.RLock()
	stale := len(w.stale)
	w.mtx.RUnlock()
	if w.Watched() != 0 || stale != dirs || len(w.armed()) != dirs {
		t.Fatalf("expected every directory to be unwanted at once and none disarmed yet, got %d stale %d armed",
			stale, len(w.armed()))
	}
	// one wanted again before it is disarmed is kept as it is, without being armed afresh
	kept := paths[0]
	rewatched := make(chan struct{})
	go func() {
		defer close(rewatched)
		w.Watch(kept)
	}()
	select {
	case <-rewatched:
	case <-time.After(5 * time.Second):
		t.Fatal("wanting back a directory that was still armed waited on the platform")
	}
	w.platform.Unlock()
	w.mtx.RLock()
	_, stillStale := w.stale[kept]
	w.mtx.RUnlock()
	if stillStale || w.Watched() != 1 || len(w.armed()) != dirs {
		t.Fatal("expected the directory to be wanted again with nothing done to the platform")
	}

	close(release)
	if !waitFor(t, 5*time.Second, func() bool { return len(w.armed()) == 1 }) {
		t.Fatalf("expected every other directory to be disarmed, %d remain armed", len(w.armed()))
	}
	if got := w.armed(); got[0] != kept {
		t.Errorf("expected the directory that was wanted again to be the one still armed, got %s", got[0])
	}
	file := filepath.Join(kept, "a.txt")
	if err := os.WriteFile(file, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return r.seen(file) }) {
		t.Error("expected events from the directory that was wanted again")
	}
	// stopping disarms everything, so nothing is left to do for what was still stale
	w.Unwatch(kept)
	w.Close()
	if len(w.stale) != 0 {
		t.Error("expected nothing to be left stale once stopped")
	}
}

// parkedDirWatcher returns a started watcher whose watch goroutine is parked in its event
// handler until release is closed, so that a test decides the order of everything that happens
func parkedDirWatcher(t *testing.T, r *eventRecorder) (w *DirWatcher, release chan struct{}) {
	t.Helper()
	parked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	o := r.options(time.Hour)
	o.OnEvent = func(path string) {
		once.Do(func() {
			close(parked)
			<-release
		})
		r.onEvent(path)
	}
	w, err := NewDirWatcher(o)
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	t.Cleanup(w.Close)
	bait := t.TempDir()
	w.Watch(bait)
	if err := os.WriteFile(filepath.Join(bait, "park.txt"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	<-parked
	return w, release
}

func (w *DirWatcher) isArmed(dir string) bool {
	return slices.Contains(w.armed(), dir)
}

func (w *DirWatcher) state(dir string) (wanted, live, stale bool) {
	w.mtx.RLock()
	defer w.mtx.RUnlock()
	_, wanted = w.dirs[dir]
	_, live = w.live[dir]
	_, stale = w.stale[dir]
	return wanted, live, stale
}

func TestDirWatcherWatchOvertakenByUnwatch(t *testing.T) {
	r := newEventRecorder()
	w, release := parkedDirWatcher(t, r)
	defer close(release)
	dir := t.TempDir()
	// a Watch that has recorded what it wants, and is kept waiting to arm it
	w.platform.Lock()
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		w.Watch(dir)
	}()
	if !waitFor(t, 5*time.Second, func() bool { return w.arming.Load() == 1 }) {
		t.Fatal("expected the Watch to be waiting to arm the directory")
	}
	// is overtaken by an Unwatch of the same directory, which has nothing armed to disarm
	w.Unwatch(dir)
	if wanted, live, stale := w.state(dir); wanted || live || stale {
		t.Fatalf("expected nothing to be left to disarm, got wanted=%t live=%t stale=%t", wanted, live, stale)
	}
	w.platform.Unlock()
	<-watched
	// the watch goroutine is parked, so nothing could have disarmed the directory if it had
	// been armed: it is unarmed because the Watch found it unwanted and left it alone
	if w.isArmed(dir) {
		t.Error("expected a directory that was unwanted by the time it could be armed not to be")
	}
	if wanted, live, stale := w.state(dir); wanted || live || stale || w.Watched() != 1 {
		t.Errorf("expected no trace of the directory, got wanted=%t live=%t stale=%t", wanted, live, stale)
	}
}

func TestDirWatcherWatchWaitsForAPendingWatch(t *testing.T) {
	r := newEventRecorder()
	w, release := parkedDirWatcher(t, r)
	dir := t.TempDir()
	w.platform.Lock()
	returned := make(chan int, 3)
	watch := func(n int) {
		go func() {
			w.Watch(dir)
			returned <- n
		}()
		if !waitFor(t, 5*time.Second, func() bool { return int(w.arming.Load()) == n }) {
			t.Fatalf("expected Watch %d to wait for the directory to be armed", n)
		}
	}
	watch(1)
	// wanted already, by a Watch that hasn't armed it yet: a second must not take that to
	// mean it is watched, and return to a caller that goes on to rely on it
	watch(2)
	// nor must one that finds it wanted again after an Unwatch, while it is still unarmed
	w.Unwatch(dir)
	watch(3)
	select {
	case n := <-returned:
		t.Fatalf("Watch %d returned before its directory was armed", n)
	default:
	}
	w.platform.Unlock()
	for range 3 {
		<-returned
	}
	if wanted, live, stale := w.state(dir); !wanted || !live || stale || !w.isArmed(dir) {
		t.Fatalf("expected the directory to be wanted and armed, got wanted=%t live=%t stale=%t", wanted, live, stale)
	}
	close(release)
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return r.seen(file) }) {
		t.Error("expected events from the directory")
	}
}

func TestDirWatcherConcurrentWatchAndUnwatch(t *testing.T) {
	r := newEventRecorder()
	w, err := NewDirWatcher(r.options(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	defer w.Close()
	parent := t.TempDir()
	dirs := make([]string, 8)
	for i := range dirs {
		dirs[i] = filepath.Join(parent, strconv.Itoa(i))
		if err := os.Mkdir(dirs[i], 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 300 {
				dir := dirs[(g+i)%len(dirs)]
				if (g+i/3)%2 == 0 {
					w.Watch(dir)
				} else {
					w.Unwatch(dir)
				}
			}
		})
	}
	wg.Wait()
	// however they interleaved, what is armed settles to exactly what is wanted
	settled := func() bool {
		w.mtx.RLock()
		pending := len(w.stale)
		wanted := slices.Sorted(maps.Keys(w.dirs))
		live := slices.Sorted(maps.Keys(w.live))
		w.mtx.RUnlock()
		armed := w.armed()
		slices.Sort(armed)
		return pending == 0 && slices.Equal(wanted, live) && slices.Equal(wanted, armed)
	}
	if !waitFor(t, 10*time.Second, settled) {
		t.Fatalf("expected what is armed to settle to what is wanted: %d wanted, %d armed", w.Watched(), len(w.armed()))
	}
	for _, dir := range dirs {
		w.Unwatch(dir)
	}
	if !waitFor(t, 10*time.Second, func() bool { return settled() && len(w.armed()) == 0 }) {
		t.Fatalf("expected nothing to be left armed, got %d", len(w.armed()))
	}
}

func TestDirWatcherWithoutEvents(t *testing.T) {
	orig := newEventWatcher
	newEventWatcher = func() (*fsnotify.Watcher, error) { return nil, errors.New("unavailable") }
	defer func() { newEventWatcher = orig }()

	r := newEventRecorder()
	w, err := NewDirWatcher(r.options(testPollInterval))
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	defer w.Close()
	w.Watch(t.TempDir())
	if !waitFor(t, 5*time.Second, func() bool { return r.intervals.Load() > 2 }) {
		t.Fatal("expected interval calls to continue without fsnotify")
	}
}

func TestDirWatcherErrors(t *testing.T) {
	r := newEventRecorder()
	w, err := NewDirWatcher(r.options(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	w.handleError(errors.New("ignored"))
	if r.lost.Load() != 0 {
		t.Error("expected only an overflow to be reported as lost events")
	}
	w.handleError(fsnotify.ErrEventOverflow)
	if r.lost.Load() != 1 {
		t.Error("expected an overflow to be reported as lost events")
	}
	w.Start()
	w.Close()
	w.mtx.RLock()
	defer w.mtx.RUnlock()
	if w.events != nil {
		t.Error("expected the event watcher to be released on Close")
	}
}

func TestDirWatcherClosedChannels(t *testing.T) {
	r := newEventRecorder()
	w, err := NewDirWatcher(r.options(testPollInterval))
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	defer w.Close()
	w.mtx.RLock()
	events := w.events
	w.mtx.RUnlock()
	// closing the event source out from under the loop must not spin or stop it
	events.Close()
	before := r.intervals.Load()
	if !waitFor(t, 5*time.Second, func() bool { return r.intervals.Load() > before+1 }) {
		t.Fatal("expected the loop to keep running after its event source closed")
	}
}
