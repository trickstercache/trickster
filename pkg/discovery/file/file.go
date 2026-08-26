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

// Package file implements the file autodiscovery provider: a watched local
// YAML or JSON member-list file, Prometheus file_sd-style. It is the
// universal escape hatch — any external service-discovery system can emit
// the member list without a bespoke in-tree provider.
//
// The file is a list of members:
//
//   - name: prom-1            # optional; defaults to the address
//     scheme: https           # optional; default http
//     address: 10.0.0.1:9090  # required host:port
//     path_prefix: /base      # optional
//     weight: 2               # optional; default 1
//
// (JSON works too — it is a subset of YAML.) Writers should replace the
// file atomically (write to a temp file, then rename); the provider watches
// the parent directory so renames are observed, coalesces change events,
// and re-reads the whole file, so a snapshot is always one complete file
// state. A file that fails to parse keeps the last-good membership. A
// low-frequency stat poll backstops filesystems with unreliable change
// notification (e.g. NFS).
package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"

	"github.com/fsnotify/fsnotify"
	"go.yaml.in/yaml/v3"
)

// ErrStopped is returned when subscribing to a stopped discoverer
var ErrStopped = errors.New("file discoverer is stopped")

const (
	// changeDebounce coalesces bursts of filesystem events into one re-read
	changeDebounce = 250 * time.Millisecond
	// statPollInterval is the fallback change-detection cadence for
	// filesystems whose notifications are unreliable
	statPollInterval = 30 * time.Second
)

// New constructs the file Discoverer; it satisfies
// discovery.NewDiscovererFunc. The file provider has no connection-level
// options; each subscription's query names the file to watch.
func New(name string, _ *do.Options) (discovery.Discoverer, error) {
	return &discoverer{name: name}, nil
}

type discoverer struct {
	name string

	mtx     sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	subs    map[*subscription]struct{}
	started bool
	stopped bool
}

func (d *discoverer) Start(ctx context.Context) error {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.stopped {
		return ErrStopped
	}
	if d.started {
		return nil
	}
	d.ctx, d.cancel = context.WithCancel(ctx)
	d.started = true
	for s := range d.subs {
		s.launch(d.ctx)
	}
	return nil
}

func (d *discoverer) Stop() error {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.stopped {
		return nil
	}
	d.stopped = true
	if d.cancel != nil {
		d.cancel()
	}
	for s := range d.subs {
		s.stop()
	}
	d.subs = nil
	return nil
}

func (d *discoverer) Subscribe(q *do.Query, handler discovery.SnapshotHandler) (func(), error) {
	if q == nil || handler == nil {
		return nil, errors.New("nil query or handler")
	}
	if q.Path == "" {
		return nil, errors.New("file discovery query requires a path")
	}
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.stopped {
		return nil, ErrStopped
	}
	s := &subscription{d: d, path: filepath.Clean(q.Path), handler: handler}
	if d.subs == nil {
		d.subs = make(map[*subscription]struct{})
	}
	d.subs[s] = struct{}{}
	if d.started {
		s.launch(d.ctx)
	}
	unsubscribe := func() {
		d.mtx.Lock()
		delete(d.subs, s)
		d.mtx.Unlock()
		s.stop()
	}
	return unsubscribe, nil
}

// memberEntry is one entry of the member-list file
type memberEntry struct {
	Name       string `yaml:"name,omitempty" json:"name,omitempty"`
	Scheme     string `yaml:"scheme,omitempty" json:"scheme,omitempty"`
	Address    string `yaml:"address" json:"address"`
	PathPrefix string `yaml:"path_prefix,omitempty" json:"path_prefix,omitempty"`
	Weight     int    `yaml:"weight,omitempty" json:"weight,omitempty"`
}

// subscription watches one member-list file
type subscription struct {
	d       *discoverer
	path    string
	handler discovery.SnapshotHandler

	mtx      sync.Mutex
	cancel   context.CancelFunc
	last     discovery.Snapshot
	hasLast  bool
	launched bool
	stopped  bool
	failing  bool
}

func (s *subscription) launch(ctx context.Context) {
	s.mtx.Lock()
	if s.launched || s.stopped {
		s.mtx.Unlock()
		return
	}
	s.launched = true
	subCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mtx.Unlock()
	go s.run(subCtx)
}

func (s *subscription) stop() {
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	s.stopped = true
	cancel := s.cancel
	s.mtx.Unlock()
	if cancel != nil {
		cancel()
	}
}

// run applies the initial file state, then re-reads on debounced filesystem
// events from the parent directory (catching atomic renames and symlink
// swaps) with a low-frequency stat poll as a fallback
func (s *subscription) run(ctx context.Context) {
	s.apply()

	var events chan fsnotify.Event
	watcher, err := fsnotify.NewWatcher()
	if err == nil {
		if werr := watcher.Add(filepath.Dir(s.path)); werr == nil {
			events = watcher.Events
		} else {
			err = werr
		}
		defer watcher.Close()
	}
	if events == nil {
		logger.Warn("file discovery watch unavailable; falling back to polling",
			logging.Pairs{
				"discoverer": s.d.name, "path": s.path, "error": err.Error(),
			})
	}

	poll := time.NewTicker(statPollInterval)
	defer poll.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	lastInfo := s.statInfo()

	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
			// any activity in the directory schedules one coalesced
			// re-read; content comparison suppresses no-op emissions
			if debounce == nil {
				debounce = time.NewTimer(changeDebounce)
				debounceC = debounce.C
			} else {
				debounce.Reset(changeDebounce)
			}
		case <-debounceC:
			debounce = nil
			debounceC = nil
			s.apply()
			lastInfo = s.statInfo()
		case <-poll.C:
			if info := s.statInfo(); info != lastInfo {
				lastInfo = info
				s.apply()
			}
		}
	}
}

// statInfo fingerprints the file for the poll fallback
func (s *subscription) statInfo() string {
	fi, err := os.Stat(s.path)
	if err != nil {
		return "absent"
	}
	return fmt.Sprintf("%d/%d", fi.Size(), fi.ModTime().UnixNano())
}

// apply reads and parses the member list and emits it when membership
// changed; read or parse failures keep the last-good membership
func (s *subscription) apply() {
	snap, err := s.read()
	if err != nil {
		s.warnRead(err)
		return
	}
	s.clearWarn()
	canonical := snap.Canonical()
	s.mtx.Lock()
	if s.stopped || (s.hasLast && canonical.Equal(s.last)) {
		s.mtx.Unlock()
		return
	}
	s.last = canonical
	s.hasLast = true
	s.mtx.Unlock()
	s.handler(canonical)
}

func (s *subscription) read() (discovery.Snapshot, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var entries []memberEntry
	if len(bytes.TrimSpace(b)) > 0 {
		if err = yaml.Unmarshal(b, &entries); err != nil {
			return nil, err
		}
	}
	out := make(discovery.Snapshot, 0, len(entries))
	for i, e := range entries {
		if e.Address == "" {
			return nil, fmt.Errorf("entry %d has no address", i)
		}
		if _, _, err := net.SplitHostPort(e.Address); err != nil {
			return nil, fmt.Errorf("entry %d address %q is not host:port",
				i, e.Address)
		}
		scheme := e.Scheme
		if scheme == "" {
			scheme = "http"
		} else if scheme != "http" && scheme != "https" {
			return nil, fmt.Errorf("entry %d scheme %q is not http or https",
				i, e.Scheme)
		}
		if e.Weight < 0 {
			return nil, fmt.Errorf("entry %d weight cannot be negative", i)
		}
		name := e.Name
		if name == "" {
			name = e.Address
		}
		out = append(out, discovery.Member{
			Name:       name,
			Scheme:     scheme,
			Address:    e.Address,
			PathPrefix: e.PathPrefix,
			Weight:     e.Weight,
			Ready:      discovery.ReadyUnknown,
		})
	}
	return out, nil
}

// warnRead logs a read/parse failure once per failure streak
func (s *subscription) warnRead(err error) {
	s.mtx.Lock()
	failing := s.failing
	s.failing = true
	s.mtx.Unlock()
	if failing {
		return
	}
	logger.Warn("file discovery read failed; keeping last-good members",
		logging.Pairs{
			"discoverer": s.d.name, "path": s.path, "error": err.Error(),
		})
}

func (s *subscription) clearWarn() {
	s.mtx.Lock()
	if s.failing {
		s.failing = false
		s.mtx.Unlock()
		logger.Info("file discovery read recovered",
			logging.Pairs{"discoverer": s.d.name, "path": s.path})
		return
	}
	s.mtx.Unlock()
}
