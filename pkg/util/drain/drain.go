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

// Package drain admits work until it is closed, then waits for what it admitted to end, no longer than a context allows.
package drain

import (
	"context"
	"sync"
	"sync/atomic"
)

// Group counts work under way as a sync.WaitGroup does, and also tells, without waiting, when none is.
type Group struct {
	active atomic.Int64
	wg     sync.WaitGroup
}

// Add adds n units of work, as sync.WaitGroup's Add does.
func (g *Group) Add(n int) {
	g.active.Add(int64(n))
	g.wg.Add(n)
}

// Done ends a unit of work.
func (g *Group) Done() {
	g.active.Add(-1)
	g.wg.Done()
}

// Wait waits for the work under way to end.
func (g *Group) Wait() { g.wg.Wait() }

// idle reports whether no work is under way.
func (g *Group) idle() bool { return g.active.Load() <= 0 }

// Gate admits work until it is closed. Its zero value is open.
type Gate struct {
	// mu orders every Add on active before Close returns, as a WaitGroup can't take an Add racing its Wait
	mu     sync.RWMutex
	closed bool
	active Group
}

// Enter admits one unit of work, which then calls Exit, or reports false once the gate is closed.
func (g *Gate) Enter() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.closed {
		return false
	}
	g.active.Add(1)
	return true
}

// Exit ends a unit of work Enter admitted.
func (g *Gate) Exit() { g.active.Done() }

// Close admits no more work. It may be called more than once.
func (g *Gate) Close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// Wait closes the gate and waits for the work it admitted to end, reporting whether it did before ctx ended.
func (g *Gate) Wait(ctx context.Context) bool {
	g.Close()
	return Wait(ctx, &g.active)
}

// Wait waits for g's work until ctx ends, reporting whether it ended first, as it has when none is under way, however ctx
// is; a goroutine left waiting on g exits once the work ends. Nothing may Add to g from zero while Wait runs.
func Wait(ctx context.Context, g *Group) bool {
	if g.idle() {
		return true
	}
	select {
	case <-Done(&g.wg):
		return true
	case <-ctx.Done():
		// work that ended as ctx did still counts as ended
		return g.idle()
	}
}

// Done returns a channel closed once wg's work ends; the goroutine that closes it exits only then.
func Done(wg *sync.WaitGroup) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}

// WaitGroup waits for wg's work until ctx ends, reporting whether it ended first. Unlike Wait, it can't see work that ends
// just as ctx does, so work that has to tell those apart counts itself with a Group.
func WaitGroup(ctx context.Context, wg *sync.WaitGroup) bool {
	done := Done(wg)
	select {
	case <-done:
		return true
	case <-ctx.Done():
		// work that ended as ctx did still counts as ended
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
}
