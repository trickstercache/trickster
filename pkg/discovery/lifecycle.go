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

package discovery

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/discovery/options"
)

// ErrStopped is returned when operating on a stopped Discoverer. Provider
// packages alias it so callers can errors.Is against either.
var ErrStopped = errors.New("discoverer is stopped")

// errNilSubscription guards Subscribe inputs
var errNilSubscription = errors.New("nil query or handler")

// SubscriptionRunner is one query's running watch/poll unit inside a
// provider. The Lifecycle owns when runners start and stop; the runner
// owns how its source is watched and when snapshots are emitted.
type SubscriptionRunner interface {
	// Launch starts the runner. The context is the discoverer's Start
	// context: its cancellation must terminate the runner's work, either
	// by observing ctx.Done or via the Stop call the Lifecycle issues.
	// Launch is called at most once.
	Launch(ctx context.Context)
	// Stop terminates the runner and releases its resources; it must be
	// idempotent and must prevent any further snapshot emission
	Stop()
}

// NewSubscriptionFunc constructs a provider-specific runner for one
// validated query. The query is already cloned; the runner may retain it.
// Deliver emissions through an Emitter wrapping the handler so no-change
// snapshots are suppressed uniformly.
type NewSubscriptionFunc func(q *options.Query, handler SnapshotHandler) (SubscriptionRunner, error)

// Lifecycle implements the shared Start/Stop/Subscribe skeleton of a
// Discoverer: subscription bookkeeping, launch-on-Start for subscriptions
// registered early, stop-everything teardown, and stopped-state guards.
// Providers supply only a NewSubscriptionFunc; see the provider authoring
// guide and the file provider for the reference usage.
type Lifecycle struct {
	name   string
	newSub NewSubscriptionFunc

	mtx     sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	subs    map[SubscriptionRunner]struct{}
	started bool
	stopped bool
}

var _ Discoverer = &Lifecycle{}

// NewLifecycle returns the shared Discoverer core for the named
// discoverer, constructing per-query runners with newSub
func NewLifecycle(name string, newSub NewSubscriptionFunc) *Lifecycle {
	return &Lifecycle{name: name, newSub: newSub}
}

// Name returns the discoverer's configured name
func (l *Lifecycle) Name() string {
	return l.name
}

func (l *Lifecycle) Start(ctx context.Context) error {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	if l.stopped {
		return fmt.Errorf("%w: %s", ErrStopped, l.name)
	}
	if l.started {
		return nil
	}
	l.ctx, l.cancel = context.WithCancel(ctx)
	l.started = true
	for s := range l.subs {
		s.Launch(l.ctx)
	}
	return nil
}

func (l *Lifecycle) Stop() error {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	if l.stopped {
		return nil
	}
	l.stopped = true
	if l.cancel != nil {
		l.cancel()
	}
	for s := range l.subs {
		s.Stop()
	}
	l.subs = nil
	return nil
}

func (l *Lifecycle) Subscribe(q *options.Query, handler SnapshotHandler) (func(), error) {
	if q == nil || handler == nil {
		return nil, errNilSubscription
	}
	l.mtx.Lock()
	defer l.mtx.Unlock()
	if l.stopped {
		return nil, fmt.Errorf("%w: %s", ErrStopped, l.name)
	}
	s, err := l.newSub(q.Clone(), handler)
	if err != nil {
		return nil, err
	}
	if l.subs == nil {
		l.subs = make(map[SubscriptionRunner]struct{})
	}
	l.subs[s] = struct{}{}
	if l.started {
		s.Launch(l.ctx)
	}
	unsubscribe := func() {
		l.mtx.Lock()
		delete(l.subs, s)
		l.mtx.Unlock()
		s.Stop()
	}
	return unsubscribe, nil
}
