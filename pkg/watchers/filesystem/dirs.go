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
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
	"github.com/trickstercache/trickster/v2/pkg/watchers"

	"github.com/fsnotify/fsnotify"
)

// DirOptions configures a DirWatcher
type DirOptions struct {
	// Name identifies the DirWatcher in log events
	Name string
	// Interval is the backstop cadence for OnInterval; it must be > 0
	Interval time.Duration
	// OnEvent is called with the path of each entry that changed in a watched
	// directory. It runs on the watch goroutine and must not block.
	OnEvent func(path string)
	// OnLost is optionally called when change events may have been dropped
	OnLost func()
	// OnInterval is optionally called at Start and on every Interval, so the
	// consumer can find changes that produced no event
	OnInterval func()
}

// ErrNoEventHandler is returned by NewDirWatcher when OnEvent is nil
var ErrNoEventHandler = errors.New("filesystem watcher: no event handler")

// DirWatcher is a restartable watchers.Watcher reporting per-entry changes in
// a set of directories that grows while it runs. It never reads file content.
type DirWatcher struct {
	opts DirOptions

	// mtx guards the watcher's state, and is never held across a call to the platform, so that
	// nothing that only changes what is wanted ever waits on one. platform serializes those calls.
	mtx      sync.RWMutex
	platform sync.Mutex
	// arming counts the callers of Watch waiting for platform, which disarming gives way to
	arming atomic.Int32
	// dirs is the directories that are wanted, and live the ones that are armed, which differ
	// while a platform call is pending. live only changes with platform held, so its holder
	// knows exactly what is armed, whatever Watch and Unwatch have been asked for meanwhile.
	dirs map[string]struct{}
	live map[string]struct{}
	// stale is the directories that are still armed but no longer wanted. They are disarmed by
	// the watch goroutine, so that Unwatch never makes its caller wait on the platform.
	stale   map[string]struct{}
	wake    chan struct{}
	running bool
	done    chan struct{}
	stopped chan struct{}
	// events is nil when stopped or when fsnotify is unavailable
	events *fsnotify.Watcher
}

var _ watchers.Watcher = &DirWatcher{}

// NewDirWatcher returns a DirWatcher for DirOptions without starting it
func NewDirWatcher(o *DirOptions) (*DirWatcher, error) {
	if o == nil {
		return nil, ErrNilOptions
	}
	if o.OnEvent == nil {
		return nil, ErrNoEventHandler
	}
	if o.Interval <= 0 {
		return nil, ErrInvalidInterval
	}
	return &DirWatcher{
		opts: *o, dirs: make(map[string]struct{}), live: make(map[string]struct{}),
		stale: make(map[string]struct{}), wake: make(chan struct{}, 1),
	}, nil
}

// Watch adds dir to the watched set. It is safe for concurrent use and may be
// called while stopped; the directory is then armed by the next Start.
func (w *DirWatcher) Watch(dir string) {
	w.mtx.RLock()
	_, wanted := w.dirs[dir]
	_, live := w.live[dir]
	w.mtx.RUnlock()
	if wanted && live {
		return
	}
	w.mtx.Lock()
	w.dirs[dir] = struct{}{}
	// armed already, it was merely unwanted until now, and is kept at no cost to the platform
	if _, live = w.live[dir]; live {
		delete(w.stale, dir)
		w.mtx.Unlock()
		return
	}
	w.mtx.Unlock()
	// wanted but not armed: by this call, or by another that is still waiting to arm it, which
	// this one then waits with, so that the directory is armed whenever Watch returns
	w.arming.Add(1)
	w.platform.Lock()
	w.arming.Add(-1)
	defer w.platform.Unlock()
	w.arm(dir)
}

// arm requires platform. It arms dir if it is still wanted and not yet armed, and reports
// false if the platform refused it, which is left to OnInterval. What was wanted when the
// caller began may not be by now, and arming it regardless would leave it armed for good.
func (w *DirWatcher) arm(dir string) bool {
	w.mtx.RLock()
	_, wanted := w.dirs[dir]
	_, live := w.live[dir]
	events := w.events
	w.mtx.RUnlock()
	if !wanted || live || events == nil {
		return true
	}
	if err := events.Add(dir); err != nil {
		logger.Debug("unable to event-watch directory",
			logging.Pairs{keys.Name: w.opts.Name, "dir": dir, keys.Detail: err.Error()})
		return false
	}
	w.mtx.Lock()
	defer w.mtx.Unlock()
	w.live[dir] = struct{}{}
	// unwanted while it was being armed, when it wasn't yet armed for Unwatch to mark as stale
	if _, wanted = w.dirs[dir]; !wanted {
		w.markStale(dir)
	}
	return true
}

// markStale requires mtx. The watch goroutine is told once, however many directories go
// stale before it looks.
func (w *DirWatcher) markStale(dir string) {
	w.stale[dir] = struct{}{}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Unwatch removes dir from the watched set. It is safe for concurrent use, and never waits on
// the platform: the directory is disarmed soon after by the watch goroutine, and until then
// events for it may still be delivered.
func (w *DirWatcher) Unwatch(dir string) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if _, ok := w.dirs[dir]; !ok {
		return
	}
	delete(w.dirs, dir)
	// one that isn't armed has nothing to disarm: a Watch still waiting to arm it will find it
	// unwanted and leave it be, and one that is arming it now marks it stale once it has
	if _, live := w.live[dir]; live {
		w.markStale(dir)
	}
}

// disarm releases the directories that are no longer wanted, one at a time, so that a Watch
// that has to arm a directory waits for one of them at most. An Unwatch waits for none.
func (w *DirWatcher) disarm() {
	for w.disarmOne() {
		// a mutex lets the goroutine that just released it take it again ahead of one that is
		// waiting, so this one stands aside for as long as a request is waiting to arm a directory
		for w.arming.Load() > 0 {
			runtime.Gosched()
		}
	}
}

func (w *DirWatcher) disarmOne() bool {
	// held before a directory is chosen, so that one chosen is disarmed before it can be armed again
	w.platform.Lock()
	defer w.platform.Unlock()
	w.mtx.Lock()
	var dir string
	var found bool
	for dir = range w.stale {
		delete(w.stale, dir)
		delete(w.live, dir)
		found = true
		break
	}
	events := w.events
	w.mtx.Unlock()
	if found && events != nil {
		// an error means the platform already dropped the watch with the directory
		_ = events.Remove(dir)
	}
	return found
}

// Watched returns the number of directories in the watched set
func (w *DirWatcher) Watched() int {
	w.mtx.RLock()
	defer w.mtx.RUnlock()
	return len(w.dirs)
}

// Start begins or resumes watching. OnInterval runs synchronously before
// Start returns. No-op if already running.
func (w *DirWatcher) Start() {
	w.mtx.Lock()
	if w.running {
		w.mtx.Unlock()
		return
	}
	prevStopped := w.stopped
	w.running = true
	done, stopped := make(chan struct{}), make(chan struct{})
	w.done, w.stopped = done, stopped
	w.mtx.Unlock()
	if prevStopped != nil {
		// wait for previous cycle's goroutine before overlapping a restart
		<-prevStopped
	}
	events := w.startEventWatches()
	if w.opts.OnInterval != nil {
		w.opts.OnInterval()
	}
	safego.Go(func(r any, stack []byte) {
		logger.Error("filesystem directory watcher goroutine panic", logging.Pairs{
			keys.Name: w.opts.Name, "panic": r, "stack": string(stack),
		})
	}, func() { w.run(events, done, stopped) })
}

// Close stops the DirWatcher and waits for its goroutine to exit. No-op if stopped.
func (w *DirWatcher) Close() {
	w.mtx.Lock()
	if !w.running {
		w.mtx.Unlock()
		return
	}
	w.running = false
	done, stopped := w.done, w.stopped
	w.mtx.Unlock()
	close(done)
	<-stopped
}

// startEventWatches best-effort arms fsnotify on the watched set; failure is interval-only.
func (w *DirWatcher) startEventWatches() *fsnotify.Watcher {
	ew, err := newEventWatcher()
	if err != nil {
		logger.Debug("fsnotify unavailable; filesystem directory watcher is interval-only",
			logging.Pairs{keys.Name: w.opts.Name, keys.Detail: err.Error()})
		return nil
	}
	w.platform.Lock()
	defer w.platform.Unlock()
	w.mtx.Lock()
	w.events = ew
	dirs := slices.Collect(maps.Keys(w.dirs))
	w.mtx.Unlock()
	for _, dir := range dirs {
		w.arm(dir)
	}
	return ew
}

// newEventWatcher is replaced in tests to simulate a platform without fsnotify
var newEventWatcher = fsnotify.NewWatcher

// rearm re-adds dropped directory watches and forgets directories that are
// gone, so a later Watch of a recreated directory arms it again.
func (w *DirWatcher) rearm() {
	w.platform.Lock()
	defer w.platform.Unlock()
	w.mtx.Lock()
	events := w.events
	if events == nil {
		w.mtx.Unlock()
		return
	}
	watched := events.WatchList()
	slices.Sort(watched)
	// a directory that was removed took its watch with it, which the platform doesn't report
	for dir := range w.live {
		if _, ok := slices.BinarySearch(watched, dir); !ok {
			delete(w.live, dir)
			delete(w.stale, dir)
		}
	}
	dirs := slices.Collect(maps.Keys(w.dirs))
	w.mtx.Unlock()
	for _, dir := range dirs {
		if !w.arm(dir) {
			w.mtx.Lock()
			delete(w.dirs, dir)
			w.mtx.Unlock()
		}
	}
}

func (w *DirWatcher) run(events *fsnotify.Watcher, done, stopped chan struct{}) {
	defer close(stopped)
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	var eventC chan fsnotify.Event
	var errC chan error
	if events != nil {
		defer func() {
			// with platform held, so that nothing is armed on an event source as it closes
			w.platform.Lock()
			defer w.platform.Unlock()
			w.mtx.Lock()
			w.events = nil
			// closing the event source disarms everything it had armed
			clear(w.stale)
			clear(w.live)
			w.mtx.Unlock()
			events.Close()
		}()
		eventC, errC = events.Events, events.Errors
	}
	for {
		select {
		case <-done:
			return
		case <-w.wake:
			w.disarm()
		case <-ticker.C:
			if w.opts.OnInterval != nil {
				w.opts.OnInterval()
			}
			w.disarm()
			w.rearm()
		case ev, ok := <-eventC:
			if !ok {
				eventC = nil
				continue
			}
			w.opts.OnEvent(ev.Name)
		case err, ok := <-errC:
			if !ok {
				errC = nil
				continue
			}
			w.handleError(err)
		}
	}
}

func (w *DirWatcher) handleError(err error) {
	if errors.Is(err, fsnotify.ErrEventOverflow) && w.opts.OnLost != nil {
		w.opts.OnLost()
		return
	}
	logger.Debug("filesystem directory watcher event error", logging.Pairs{
		keys.Name: w.opts.Name, keys.Detail: err.Error(),
	})
}
