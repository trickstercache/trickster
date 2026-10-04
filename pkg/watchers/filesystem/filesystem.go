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

// Package filesystem provides a watchers.Watcher using fsnotify plus a
// content-comparing poll backstop; watches parent dirs for atomic renames.
package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
	"github.com/trickstercache/trickster/v2/pkg/watchers"

	"github.com/fsnotify/fsnotify"
)

// FailureThreshold consecutive failed checks before escalating to a WARN log.
const FailureThreshold = 5

// eventDebounce coalesces filesystem event bursts into one check.
const eventDebounce = 250 * time.Millisecond

// Options configures a filesystem Watcher
type Options struct {
	// Name identifies the Watcher in log events
	Name string
	// Paths is the set of files watched and delivered as one unit
	Paths []string
	// Interval is the backstop poll interval; it must be > 0
	Interval time.Duration
	// OnChange is called with Path-ordered contents when the set changes,
	// including Start's initial read. An error rejects and retries later.
	OnChange func(contents [][]byte) error
	// OnReadError is optionally invoked when a check fails to read a file
	OnReadError func(error)
	// SkipUnchanged has polls skip reading files whose size and mtime are unchanged since the last read;
	// events always read, and a file that changes mid-read is refused
	SkipUnchanged bool
}

// errors returned by New
var (
	ErrNilOptions      = errors.New("filesystem watcher: nil options")
	ErrNoPaths         = errors.New("filesystem watcher: no paths to watch")
	ErrInvalidInterval = errors.New("filesystem watcher: interval must be > 0")
)

// ErrChangedDuringRead is passed to OnReadError when SkipUnchanged finds a file changed while it was read
var ErrChangedDuringRead = errors.New("filesystem watcher: file changed while it was read")

type fileStat struct {
	size, modNanos int64
}

// Watcher is a restartable filesystem watchers.Watcher. After Close, Start
// resumes and delivers any missed content change via its initial check.
type Watcher struct {
	opts      Options
	eventDirs []string

	// lastApplied/consecutiveFailures: Start/Close exclusivity (Close waits
	// for goroutine exit) makes access safe without additional locking
	lastApplied         string
	consecutiveFailures int
	lastStats           []fileStat // at the last read, for SkipUnchanged
	readFile            func(string) ([]byte, error)

	mtx     sync.Mutex
	running bool
	done    chan struct{}
	stopped chan struct{}
	// events is nil when fsnotify is unavailable; detection is poll-only
	events *fsnotify.Watcher
}

var _ watchers.Watcher = &Watcher{}

// New returns a Watcher for Options without starting it; call Start to begin.
func New(o *Options) (*Watcher, error) {
	if o == nil {
		return nil, ErrNilOptions
	}
	if len(o.Paths) == 0 {
		return nil, ErrNoPaths
	}
	if o.Interval <= 0 {
		return nil, ErrInvalidInterval
	}
	w := &Watcher{opts: *o, readFile: os.ReadFile}
	w.opts.Paths = slices.Clone(o.Paths)
	for _, path := range w.opts.Paths {
		dir := filepath.Dir(path)
		if !slices.Contains(w.eventDirs, dir) {
			w.eventDirs = append(w.eventDirs, dir)
		}
	}
	return w, nil
}

// StartNew returns a started Watcher. OnChange may run before StartNew returns
// if content is currently readable and accepted.
func StartNew(o *Options) (*Watcher, error) {
	w, err := New(o)
	if err != nil {
		return nil, err
	}
	w.Start()
	return w, nil
}

// Start begins or resumes watching. The initial check runs synchronously and
// may deliver OnChange before Start returns. No-op if already running.
func (w *Watcher) Start() {
	w.mtx.Lock()
	if w.running {
		w.mtx.Unlock()
		return
	}
	prevStopped := w.stopped
	w.running = true
	w.done = make(chan struct{})
	w.stopped = make(chan struct{})
	w.mtx.Unlock()
	if prevStopped != nil {
		// wait for previous cycle's goroutine before overlapping a restart
		<-prevStopped
	}
	w.check(true)
	w.startEventWatches()
	safego.Go(func(r any, stack []byte) {
		logger.Error("filesystem watcher goroutine panic", logging.Pairs{
			"name": w.opts.Name, "panic": r, "stack": string(stack),
		})
	}, w.run)
}

// Close stops the Watcher and waits for its goroutine to exit. No-op if stopped.
func (w *Watcher) Close() {
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

// startEventWatches best-effort arms fsnotify on parent dirs; failure is poll-only.
func (w *Watcher) startEventWatches() {
	ew, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Debug("fsnotify unavailable; filesystem watcher is poll-only",
			logging.Pairs{keys.Name: w.opts.Name, keys.Detail: err.Error()})
		return
	}
	added := 0
	for _, dir := range w.eventDirs {
		if err := ew.Add(dir); err != nil {
			logger.Debug("unable to event-watch directory",
				logging.Pairs{keys.Name: w.opts.Name, "dir": dir, keys.Detail: err.Error()})
			continue
		}
		added++
	}
	if added == 0 {
		ew.Close()
		return
	}
	w.events = ew
}

// rearmEventWatches re-adds dropped directory watches on the poll cadence.
func (w *Watcher) rearmEventWatches() {
	if w.events == nil {
		return
	}
	watched := w.events.WatchList()
	for _, dir := range w.eventDirs {
		if !slices.Contains(watched, dir) {
			_ = w.events.Add(dir) // best-effort; retried on the next tick
		}
	}
}

func (w *Watcher) run() {
	defer close(w.stopped)
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	var eventC chan fsnotify.Event
	var errC chan error
	if events := w.events; events != nil {
		defer func() {
			events.Close()
			w.events = nil
		}()
		eventC = events.Events
		errC = events.Errors
	}
	var debounce *time.Timer
	var debounceC <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()
	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			w.check(false)
			w.rearmEventWatches()
		case <-debounceC:
			debounceC = nil
			w.check(true)
		case _, ok := <-eventC:
			if !ok {
				eventC = nil
				continue
			}
			// any watched-dir event schedules a content-comparing check
			if debounceC == nil {
				debounce = time.NewTimer(eventDebounce)
				debounceC = debounce.C
			}
		case err, ok := <-errC:
			if !ok {
				errC = nil
				continue
			}
			logger.Debug("filesystem watcher event error", logging.Pairs{
				"name": w.opts.Name, "detail": err.Error(),
			})
		}
	}
}

// check reads the set and applies OnChange on content change. Read errors and
// rejections leave last-accepted content in effect; deletion is a read failure.
func (w *Watcher) check(forced bool) {
	var before []fileStat
	if w.opts.SkipUnchanged {
		var err error
		if before, err = w.statAll(); err != nil {
			w.readFailed(err)
			return
		}
		if !forced && slices.Equal(before, w.lastStats) {
			w.consecutiveFailures = 0
			return
		}
	}
	contents := make([][]byte, len(w.opts.Paths))
	hash := sha256.New()
	for i, path := range w.opts.Paths {
		b, err := w.readFile(path)
		if err != nil {
			w.readFailed(err)
			return
		}
		contents[i] = b
		hash.Write(b)
	}
	if w.opts.SkipUnchanged {
		after, err := w.statAll()
		if err == nil && !slices.Equal(before, after) {
			err = ErrChangedDuringRead
		}
		if err != nil {
			w.readFailed(err)
			return
		}
		w.lastStats = after
	}
	setHash := hex.EncodeToString(hash.Sum(nil))
	if setHash == w.lastApplied {
		w.consecutiveFailures = 0
		return
	}
	if w.opts.OnChange != nil {
		if err := w.opts.OnChange(contents); err != nil {
			w.recordFailure("filesystem watcher change rejected", err)
			return
		}
	}
	w.lastApplied = setHash
	w.consecutiveFailures = 0
}

func (w *Watcher) statAll() ([]fileStat, error) {
	stats := make([]fileStat, len(w.opts.Paths))
	for i, path := range w.opts.Paths {
		fi, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		stats[i] = fileStat{size: fi.Size(), modNanos: fi.ModTime().UnixNano()}
	}
	return stats, nil
}

func (w *Watcher) readFailed(err error) {
	if w.opts.OnReadError != nil {
		w.opts.OnReadError(err)
	}
	w.recordFailure("filesystem watcher unable to read watched file", err)
}

func (w *Watcher) recordFailure(event string, err error) {
	w.consecutiveFailures++
	if w.consecutiveFailures == FailureThreshold {
		logger.Warn(event, logging.Pairs{
			"name": w.opts.Name, "detail": err.Error(),
			"consecutiveFailures": w.consecutiveFailures,
		})
	}
}
