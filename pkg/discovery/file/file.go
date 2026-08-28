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
// file atomically (write to a temp file, then rename). Change detection is
// delegated to watchers/filesystem: parent-directory event watching (so
// renames and symlink swaps are observed) debounced into whole-file
// re-reads, backed by a content-comparing poll (file.poll_interval) that
// is the effective mechanism on filesystems without reliable change
// notification. A file that fails to read or parse keeps the last-good
// membership and is retried.
package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/watchers/filesystem"

	"go.yaml.in/yaml/v3"
)

// ErrStopped aliases discovery.ErrStopped for callers of this package
var ErrStopped = discovery.ErrStopped

// provider carries the file provider's connection-level settings; the
// shared discovery.Lifecycle owns Start/Stop/Subscribe
type provider struct {
	name string
	// pollInterval is the content-comparing poll cadence; on filesystems
	// without reliable change notification it is the effective update
	// mechanism
	pollInterval time.Duration
}

// New constructs the file Discoverer; it satisfies
// discovery.NewDiscovererFunc. Each subscription's query names the file to
// watch; the optional file options block tunes the change-detection poll
// cadence (see options.FileOptions).
func New(name string, o *do.Options) (discovery.Discoverer, error) {
	p := &provider{name: name, pollInterval: pollIntervalFor(o)}
	return discovery.NewLifecycle(name, p.newSubscription), nil
}

// pollIntervalFor resolves the configured poll cadence with its default
func pollIntervalFor(o *do.Options) time.Duration {
	if o != nil && o.File != nil && o.File.PollInterval > 0 {
		return time.Duration(o.File.PollInterval)
	}
	return do.DefaultFilePollInterval
}

// newSubscription validates the query and builds its runner; it satisfies
// discovery.NewSubscriptionFunc
func (p *provider) newSubscription(q *do.Query, handler discovery.SnapshotHandler) (discovery.SubscriptionRunner, error) {
	if q.Path == "" {
		return nil, errors.New("file discovery query requires a path")
	}
	return &subscription{
		p:       p,
		path:    filepath.Clean(q.Path),
		emitter: discovery.NewEmitter(handler),
	}, nil
}

// memberEntry is one entry of the member-list file
type memberEntry struct {
	Name       string `yaml:"name,omitempty" json:"name,omitempty"`
	Scheme     string `yaml:"scheme,omitempty" json:"scheme,omitempty"`
	Address    string `yaml:"address" json:"address"`
	PathPrefix string `yaml:"path_prefix,omitempty" json:"path_prefix,omitempty"`
	Weight     int    `yaml:"weight,omitempty" json:"weight,omitempty"`
	// ReplicaGroup optionally assigns the member to a TSM replica group
	ReplicaGroup string `yaml:"replica_group,omitempty" json:"replica_group,omitempty"`
}

// subscription binds one member-list file's filesystem Watcher to the
// snapshot handler; it implements discovery.SubscriptionRunner
type subscription struct {
	p       *provider
	path    string
	emitter *discovery.Emitter

	mtx     sync.Mutex
	watcher *filesystem.Watcher
	stopCtx func() bool
	stopped bool
	failing bool
}

// Launch starts the subscription's filesystem Watcher; the Watcher owns
// event watching, debounce, and the content-comparing poll, and runs its
// initial check (which may deliver the first snapshot) before returning
func (s *subscription) Launch(ctx context.Context) {
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	w, err := filesystem.New(&filesystem.Options{
		Name:        s.p.name + ":" + s.path,
		Paths:       []string{s.path},
		Interval:    s.p.pollInterval,
		OnChange:    s.onChange,
		OnReadError: s.warnRead,
	})
	if err != nil {
		// unreachable with a validated subscription (non-empty path,
		// positive interval), but never fail silently
		s.mtx.Unlock()
		discovery.LogError("file discovery watcher construction failed",
			logging.Pairs{
				"discoverer": s.p.name, "path": s.path, "error": err.Error(),
			})
		return
	}
	s.watcher = w
	// the discoverer's Start context also terminates the subscription
	s.stopCtx = context.AfterFunc(ctx, s.Stop)
	s.mtx.Unlock()
	w.Start()
}

// Stop terminates the watcher and suppresses further emissions
func (s *subscription) Stop() {
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	s.stopped = true
	w := s.watcher
	stopCtx := s.stopCtx
	s.mtx.Unlock()
	s.emitter.Stop()
	if stopCtx != nil {
		stopCtx()
	}
	if w != nil {
		w.Close()
	}
}

// onChange receives the watched file's contents from the Watcher whenever
// they differ from the last accepted state; a parse or validation error
// rejects the change (keeping the last-good membership) and the Watcher
// retries it on subsequent checks
func (s *subscription) onChange(contents [][]byte) error {
	snap, err := parseMembers(contents[0])
	if err != nil {
		s.warnRead(err)
		return err
	}
	s.clearWarn()
	// the Watcher already suppresses byte-identical content; the Emitter
	// additionally suppresses semantic no-ops (reordered or reformatted
	// entries)
	s.emitter.Emit(snap)
	return nil
}

// parseMembers converts member-list file content into a Snapshot
func parseMembers(b []byte) (discovery.Snapshot, error) {
	var entries []memberEntry
	if len(bytes.TrimSpace(b)) > 0 {
		if err := yaml.Unmarshal(b, &entries); err != nil {
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
			Name:         name,
			Scheme:       scheme,
			Address:      e.Address,
			PathPrefix:   e.PathPrefix,
			Weight:       e.Weight,
			ReplicaGroup: e.ReplicaGroup,
			Ready:        discovery.ReadyUnknown,
		})
	}
	return out, nil
}

// warnRead counts a read/parse failure and logs it once per failure streak
func (s *subscription) warnRead(err error) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.File).Inc()
	s.mtx.Lock()
	failing := s.failing
	s.failing = true
	s.mtx.Unlock()
	if failing {
		return
	}
	discovery.LogWarn("file discovery read failed; keeping last-good members",
		logging.Pairs{
			"discoverer": s.p.name, "path": s.path, "error": err.Error(),
		})
}

func (s *subscription) clearWarn() {
	s.mtx.Lock()
	if s.failing {
		s.failing = false
		s.mtx.Unlock()
		discovery.LogInfo("file discovery read recovered",
			logging.Pairs{"discoverer": s.p.name, "path": s.path})
		return
	}
	s.mtx.Unlock()
}
