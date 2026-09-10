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

package signaling

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config/reload"
)

// Wait blocks until ctx ends or SIGINT/SIGTERM arrives, then returns a channel
// that closes once every reload it started has finished. SIGHUP runs reloader
// off the loop; a SIGHUP arriving during a reload schedules exactly one
// follow-up reload after it. onTerminate runs the instant termination is
// received; the caller decides how long to wait for in-flight reloads.
func Wait(ctx context.Context, reloader reload.Reloader, onTerminate func()) <-chan struct{} {
	sigs := make(chan os.Signal, 1)
	// Defers run LIFO: signal.Stop unregisters our channel from os/signal's
	// fanout before close runs, so no send-on-closed-channel panic can occur
	// if a signal arrives while we're tearing down.
	defer close(sigs)
	defer signal.Stop(sigs)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	reloads := &reloadQueue{reloader: reloader}
	for {
		select {
		case <-ctx.Done():
			return reloads.done()
		case sig := <-sigs:
			switch sig {
			case syscall.SIGHUP:
				reloads.request()
			case syscall.SIGINT, syscall.SIGTERM:
				if onTerminate != nil {
					onTerminate()
				}
				return reloads.done()
			}
		}
	}
}

// reloadQueue runs reloads one at a time off the signal loop, coalescing
// requests that arrive during a reload into a single follow-up run.
type reloadQueue struct {
	reloader reload.Reloader
	mtx      sync.Mutex
	running  bool
	pending  bool
	wg       sync.WaitGroup
}

func (q *reloadQueue) request() {
	q.mtx.Lock()
	defer q.mtx.Unlock()
	if q.running {
		q.pending = true
		return
	}
	q.running = true
	q.wg.Go(q.run)
}

func (q *reloadQueue) run() {
	for {
		q.reloader(reload.SourceSIGHUP)
		q.mtx.Lock()
		if !q.pending {
			q.running = false
			q.mtx.Unlock()
			return
		}
		q.pending = false
		q.mtx.Unlock()
	}
}

// done returns a channel closed once no reload is running or pending.
func (q *reloadQueue) done() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(ch)
	}()
	return ch
}

// DrainContext returns a context that ends when timeout elapses or when a
// further SIGINT or SIGTERM arrives, so an operator can cut a shutdown short.
func DrainContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		defer signal.Stop(sigs)
		select {
		case <-sigs:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
